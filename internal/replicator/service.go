package replicator

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

type Service struct {
	cfg       Config
	log       *slog.Logger
	store     *Store
	tracker   *TrackerClient
	guardians map[string]*DaemonClient
}

func NewService(cfg Config, logger *slog.Logger) (*Service, error) {
	if cfg.TrackerURL == "" {
		return nil, errors.New("REPLICATOR_TRACKER_URL is required")
	}
	if len(cfg.DaemonEndpoints) == 0 {
		return nil, errors.New("REPLICATOR_DAEMON_ENDPOINTS is required")
	}
	store, err := NewStore(cfg.StateFilePath)
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	guardians := make(map[string]*DaemonClient, len(cfg.DaemonEndpoints))
	for _, endpoint := range cfg.DaemonEndpoints {
		guardians[endpoint] = NewDaemonClient(endpoint, cfg.LiveAgentAPIKey)
	}
	return &Service{
		cfg:       cfg,
		log:       logger,
		store:     store,
		tracker:   NewTrackerClient(cfg.TrackerURL),
		guardians: guardians,
	}, nil
}

func (s *Service) Run(ctx context.Context) error {
	s.log.Info("replicator started",
		"tracker_url", s.cfg.TrackerURL,
		"daemon_endpoints", s.cfg.DaemonEndpoints,
		"poll_interval", s.cfg.PollInterval.String(),
		"replica_target", s.cfg.ReplicaTarget,
		"state_file", s.cfg.StateFilePath,
	)
	// Initial catch-up avoids missing announcements while the replicator was down.
	if err := s.pollOnce(ctx); err != nil {
		s.log.Warn("replicator startup poll failed", "error", err)
	}
	ticker := time.NewTicker(s.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := s.pollOnce(ctx); err != nil {
				s.log.Warn("replicator poll failed", "error", err)
			}
		}
	}
}

func (s *Service) pollOnce(ctx context.Context) error {
	watermark := s.store.Watermark()
	if watermark.IsZero() {
		watermark = time.Now().Add(-s.cfg.RecentAssetsLookback)
	}
	assets, err := s.tracker.RecentAssets(ctx, watermark, s.cfg.RecentAssetsLimit)
	if err != nil {
		return err
	}
	latest := watermark
	for _, asset := range assets {
		if asset.AnnouncedAt.After(latest) {
			latest = asset.AnnouncedAt
		}
		if err := s.ensureReplicated(ctx, asset); err != nil {
			s.log.Warn("replication failed", "cid", asset.CID, "error", err)
		}
	}
	if latest.After(watermark) {
		if err := s.store.SetWatermark(latest); err != nil {
			return err
		}
	}
	s.log.Info("poll complete",
		"recent_assets", len(assets),
		"watermark_utc", latest.UTC().Format(time.RFC3339),
	)
	return nil
}

func (s *Service) ensureReplicated(ctx context.Context, asset RecentAsset) error {
	job := s.store.GetJob(asset.CID)
	if job == nil {
		job = &JobState{
			CID:             asset.CID,
			Filename:        asset.Filename,
			AnnouncedAt:     asset.AnnouncedAt.UTC().Format(time.RFC3339),
			StatusByNode:    map[string]string{},
			AttemptsByNode:  map[string]int{},
			LastErrorByNode: map[string]string{},
		}
	}

	replicaCount, err := s.tracker.ReplicaCount(ctx, asset.CID, s.cfg.MaxSourcePeerAge)
	if err != nil {
		return err
	}
	if replicaCount >= s.cfg.ReplicaTarget {
		for node := range s.guardians {
			if _, ok := job.StatusByNode[node]; !ok {
				job.StatusByNode[node] = string(statusSkipped)
			}
		}
		return s.store.UpsertJob(job)
	}

	for node, guardian := range s.guardians {
		if replicaCount >= s.cfg.ReplicaTarget {
			break
		}
		if job.StatusByNode[node] == string(statusCompleted) {
			continue
		}
		if err := s.ensureGuardianCapacity(ctx, guardian); err != nil {
			job.StatusByNode[node] = string(statusFailed)
			job.LastErrorByNode[node] = err.Error()
			continue
		}

		job.StatusByNode[node] = string(statusRunning)
		job.AttemptsByNode[node] = job.AttemptsByNode[node] + 1
		if err := s.store.UpsertJob(job); err != nil {
			return err
		}

		if err := guardian.QueueDownload(ctx, asset.CID); err != nil {
			job.StatusByNode[node] = string(statusFailed)
			job.LastErrorByNode[node] = err.Error()
			_ = s.store.UpsertJob(job)
			continue
		}
		if err := s.waitForDownload(ctx, guardian, asset.CID); err != nil {
			job.StatusByNode[node] = string(statusFailed)
			job.LastErrorByNode[node] = err.Error()
			_ = s.store.UpsertJob(job)
			continue
		}

		job.StatusByNode[node] = string(statusCompleted)
		delete(job.LastErrorByNode, node)
		if err := s.store.UpsertJob(job); err != nil {
			return err
		}
		s.log.Info("guardian download completed", "cid", asset.CID, "daemon", node)

		replicaCount, err = s.tracker.ReplicaCount(ctx, asset.CID, s.cfg.MaxSourcePeerAge)
		if err != nil {
			return err
		}
	}
	return s.store.UpsertJob(job)
}

func (s *Service) ensureGuardianCapacity(ctx context.Context, guardian *DaemonClient) error {
	used, err := guardian.LibraryUsage(ctx)
	if err != nil {
		return err
	}
	if used >= s.cfg.StorageHardLimitBytes {
		return errors.New("guardian storage hard limit reached")
	}
	if used >= s.cfg.StorageSoftLimitBytes {
		// Safe-eviction policy placeholder: never evict if it may drop global replicas below target.
		// Current daemon API has no delete/unshare endpoint, so we block new replication above soft cap.
		return errors.New("guardian storage soft limit reached; eviction policy blocked")
	}
	return nil
}

func (s *Service) waitForDownload(ctx context.Context, guardian *DaemonClient, cid string) error {
	timeoutCtx, cancel := context.WithTimeout(ctx, s.cfg.DownloadTimeout)
	defer cancel()
	ticker := time.NewTicker(s.cfg.DownloadPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-timeoutCtx.Done():
			return timeoutCtx.Err()
		case <-ticker.C:
			state, err := guardian.DownloadState(timeoutCtx, cid)
			if err != nil {
				continue
			}
			switch state {
			case "completed":
				return nil
			case "failed":
				return errors.New("download failed on guardian")
			}
		}
	}
}
