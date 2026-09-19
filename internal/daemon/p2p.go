// Package: internal/daemon
// Feature: F-010 (Go Core Daemon)
// Story: US-010-04 (DHT/GossipSub/mDNS Integration)
// Purpose: P2P networking with DHT peer discovery - Built with TDD

package daemon

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"
	dutil "github.com/libp2p/go-libp2p/p2p/discovery/util"
	"github.com/libp2p/go-libp2p/p2p/host/autorelay"
	"github.com/libp2p/go-libp2p/p2p/net/swarm"
	circuitv2client "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	"github.com/multiformats/go-multiaddr"
	"github.com/stonkagents/agent/internal/logger"
	blockprotocol "github.com/stonkagents/agent/pkg/protocol"
)

// P2PHost wraps a libp2p host with DHT, GossipSub, and mDNS
type P2PHost struct {
	host             host.Host
	dht              *dht.IpfsDHT
	pubsub           *pubsub.PubSub
	mdnsService      mdns.Service
	blockExchangeSvc blockprotocol.BlockExchangeService
	subscriptions    map[string]*pubsub.Subscription
	subMutex         sync.RWMutex
	logger           *logger.Logger
	ctx              context.Context
	cancel           context.CancelFunc
	relayInfo        *peer.AddrInfo // Tracker relay for circuit fallback in ConnectToPeer
}

// discoveryNotifee gets notified when we find a peer via mDNS
type discoveryNotifee struct {
	h host.Host
}

// HandlePeerFound connects to peers discovered via mDNS
func (n *discoveryNotifee) HandlePeerFound(pi peer.AddrInfo) {
	n.h.Connect(context.Background(), pi)
}

// NewP2PHost creates a new P2P host with DHT, GossipSub, and mDNS.
// If relayInfo is non-nil, configures AutoRelay with the given relay as a static relay
// and forces reachability to private (skips AutoNAT wait, immediately uses relay).
func NewP2PHost(config *Config, log *logger.Logger, relayInfo ...peer.AddrInfo) (*P2PHost, error) {
	ctx, cancel := context.WithCancel(context.Background())

	var privKey crypto.PrivKey
	if config != nil && config.PrivateKey != "" {
		decoded, err := base64.StdEncoding.DecodeString(config.PrivateKey)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("failed to decode private key: %w", err)
		}
		privKey, err = crypto.UnmarshalEd25519PrivateKey(decoded)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("failed to unmarshal ed25519 private key: %w", err)
		}
	} else {
		// Fallback: generate a new Ed25519 keypair (non-persistent)
		var err error
		privKey, _, err = crypto.GenerateKeyPair(crypto.Ed25519, -1)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("failed to generate key pair: %w", err)
		}
	}

	// Build libp2p options — relay configuration depends on whether we have a static relay
	opts := []libp2p.Option{
		libp2p.Identity(privKey),
		libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0"), // Random port
		libp2p.NATPortMap(),                            // Enable UPnP/NAT-PMP port mapping
		libp2p.EnableHolePunching(),                    // DCUtR: upgrade relay connections to direct
	}

	if len(relayInfo) > 0 && len(relayInfo[0].Addrs) > 0 {
		// Static relay available: use AutoRelay with ForceReachabilityPrivate
		// This immediately advertises relay circuit addresses without waiting for AutoNAT
		opts = append(opts,
			libp2p.EnableAutoRelayWithStaticRelays(relayInfo, autorelay.WithNumRelays(1)),
			libp2p.ForceReachabilityPrivate(),
		)
		if log != nil {
			log.Info("P2P", "NewP2PHost", "Configuring AutoRelay with static tracker relay", map[string]interface{}{
				"relay_peer_id": relayInfo[0].ID.String(),
				"relay_addrs":   len(relayInfo[0].Addrs),
			})
		}
	} else {
		// No static relay: fall back to basic relay + AutoNAT (original behavior)
		opts = append(opts,
			libp2p.EnableRelay(),
			libp2p.EnableAutoNATv2(),
		)
	}

	// Create libp2p host with DHT routing
	var kadDHT *dht.IpfsDHT

	h, err := libp2p.New(opts...)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create libp2p host: %w", err)
	}

	// Create DHT
	kadDHT, err = dht.New(ctx, h)
	if err != nil {
		cancel()
		h.Close()
		return nil, fmt.Errorf("failed to create DHT: %w", err)
	}

	// Create GossipSub pubsub
	ps, err := pubsub.NewGossipSub(ctx, h)
	if err != nil {
		cancel()
		h.Close()
		return nil, fmt.Errorf("failed to create GossipSub: %w", err)
	}

	// Store relay info for explicit circuit fallback in ConnectToPeer
	var storedRelayInfo *peer.AddrInfo
	if len(relayInfo) > 0 && len(relayInfo[0].Addrs) > 0 {
		info := relayInfo[0] // copy
		storedRelayInfo = &info
	}

	p2pHost := &P2PHost{
		host:          h,
		dht:           kadDHT,
		pubsub:        ps,
		subscriptions: make(map[string]*pubsub.Subscription),
		logger:        log,
		ctx:           ctx,
		cancel:        cancel,
		relayInfo:     storedRelayInfo,
	}

	// Bootstrap DHT
	if err = kadDHT.Bootstrap(ctx); err != nil {
		cancel()
		h.Close()
		return nil, fmt.Errorf("failed to bootstrap DHT: %w", err)
	}

	// Setup mDNS for local peer discovery
	mdnsService := mdns.NewMdnsService(h, "StonkAgents", &discoveryNotifee{h: h})
	p2pHost.mdnsService = mdnsService

	// Connect to bootstrap nodes in background
	go p2pHost.connectBootstrapPeers()

	// Start peer discovery
	go p2pHost.discoverPeers()

	// Explicitly connect to relay and reserve a slot for observability.
	// AutoRelay handles this automatically but provides no error feedback.
	if storedRelayInfo != nil {
		go p2pHost.ensureRelayReservation(*storedRelayInfo)
	}

	if log != nil {
		log.Info("P2P", "NewP2PHost", "libp2p host created with DHT/GossipSub/mDNS", map[string]interface{}{
			"peer_id": h.ID().String(),
			"addrs":   h.Addrs(),
		})
	}

	return p2pHost, nil
}

// connectBootstrapPeers connects to IPFS bootstrap nodes
func (p *P2PHost) connectBootstrapPeers() {
	// Use IPFS public bootstrap nodes for MVP
	bootstrapPeers := []string{
		"/dnsaddr/bootstrap.libp2p.io/p2p/QmNnooDu7bfjPFoTZYxMNLWUQJyrVwtbZg5gBMjTezGAJN",
		"/dnsaddr/bootstrap.libp2p.io/p2p/QmQCU2EcMqAqQPR2i9bChDtGNJchTbq5TbXJJ16u19uLTa",
		"/dnsaddr/bootstrap.libp2p.io/p2p/QmbLHAnMoJPWSCR5Zhtx6BHJX9KiKNN6tpvbUcqanj75Nb",
		"/dnsaddr/bootstrap.libp2p.io/p2p/QmcZf59bWwK5XFi76CZX8cbJ4BhTzzA3gU1ZjYZcYW3dwt",
	}

	for _, peerAddr := range bootstrapPeers {
		addr, err := multiaddr.NewMultiaddr(peerAddr)
		if err != nil {
			continue
		}

		peerinfo, err := peer.AddrInfoFromP2pAddr(addr)
		if err != nil {
			continue
		}

		if err := p.host.Connect(p.ctx, *peerinfo); err != nil {
			// Ignore connection failures to bootstrap nodes (may not be reachable in test env)
			if p.logger != nil {
				p.logger.Debug("P2P", "connectBootstrapPeers", "Failed to connect to bootstrap peer", map[string]interface{}{
					"peer":  peerinfo.ID.String(),
					"error": err.Error(),
				})
			}
		} else {
			if p.logger != nil {
				p.logger.Info("P2P", "connectBootstrapPeers", "Connected to bootstrap peer", map[string]interface{}{
					"peer": peerinfo.ID.String(),
				})
			}
		}
	}
}

// ensureRelayReservation explicitly connects to the relay and establishes a reservation.
// This complements AutoRelay by providing immediate feedback on success/failure.
func (p *P2PHost) ensureRelayReservation(relayInfo peer.AddrInfo) {
	// Wait for host initialization to settle
	time.Sleep(3 * time.Second)

	// Connect to the relay
	connectCtx, connectCancel := context.WithTimeout(p.ctx, 15*time.Second)
	defer connectCancel()
	if err := p.host.Connect(connectCtx, relayInfo); err != nil {
		if p.logger != nil {
			p.logger.Error("P2P", "ensureRelayReservation", "Failed to connect to relay", map[string]interface{}{
				"relay_id": relayInfo.ID.String(),
				"error":    err.Error(),
			})
		}
		return
	}

	if p.logger != nil {
		p.logger.Info("P2P", "ensureRelayReservation", "Connected to relay, requesting reservation", map[string]interface{}{
			"relay_id": relayInfo.ID.String(),
		})
	}

	// Request a reservation on the relay
	reserveCtx, reserveCancel := context.WithTimeout(p.ctx, 15*time.Second)
	defer reserveCancel()
	rsvp, err := circuitv2client.Reserve(reserveCtx, p.host, relayInfo)
	if err != nil {
		if p.logger != nil {
			p.logger.Error("P2P", "ensureRelayReservation", "Relay reservation FAILED", map[string]interface{}{
				"relay_id": relayInfo.ID.String(),
				"error":    err.Error(),
			})
		}
		return
	}

	if p.logger != nil {
		p.logger.Info("P2P", "ensureRelayReservation", "Relay reservation established", map[string]interface{}{
			"relay_id":   relayInfo.ID.String(),
			"expires":    rsvp.Expiration.Format(time.RFC3339),
			"addrs":      rsvp.Addrs,
			"host_addrs": p.host.Addrs(),
		})
	}
}

// discoverPeers continuously discovers peers via DHT
func (p *P2PHost) discoverPeers() {
	routingDiscovery := drouting.NewRoutingDiscovery(p.dht)
	dutil.Advertise(p.ctx, routingDiscovery, "StonkAgents")

	ticker := time.NewTicker(time.Second * 30)
	defer ticker.Stop()

	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			peerChan, err := routingDiscovery.FindPeers(p.ctx, "StonkAgents")
			if err != nil {
				continue
			}

			for peerInfo := range peerChan {
				if peerInfo.ID == p.host.ID() {
					continue
				}
				p.host.Connect(p.ctx, peerInfo)
			}
		}
	}
}

// ID returns the PeerID
func (p *P2PHost) ID() peer.ID {
	return p.host.ID()
}

// PublicKeyBase64 returns the host's Ed25519 public key as base64 (same format as genkeys / config public_key).
func (p *P2PHost) PublicKeyBase64() (string, error) {
	pubKey := p.host.Peerstore().PubKey(p.host.ID())
	if pubKey == nil {
		return "", fmt.Errorf("no public key in peerstore for self")
	}
	raw, err := pubKey.Raw()
	if err != nil {
		return "", fmt.Errorf("public key raw: %w", err)
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// Addrs returns the host's multiaddrs
func (p *P2PHost) Addrs() []string {
	addrs := p.host.Addrs()
	strAddrs := make([]string, len(addrs))
	for i, addr := range addrs {
		strAddrs[i] = addr.String()
	}
	return strAddrs
}

// AnnounceAddrs returns multiaddrs with /p2p/<peerID> appended for tracker registration.
// Filters out loopback and link-local addresses, keeps private LAN + public + relay addrs.
func (p *P2PHost) AnnounceAddrs() []string {
	peerID := p.host.ID().String()
	rawAddrs := p.host.Addrs()
	announceAddrs := make([]string, 0, len(rawAddrs))
	for _, addr := range rawAddrs {
		addrStr := addr.String()
		// Skip loopback addresses (127.x.x.x, ::1)
		if strings.HasPrefix(addrStr, "/ip4/127.") || strings.HasPrefix(addrStr, "/ip6/::1/") {
			continue
		}
		// Skip link-local addresses (169.254.x.x, fe80::)
		if strings.HasPrefix(addrStr, "/ip4/169.254.") || strings.HasPrefix(addrStr, "/ip6/fe80") {
			continue
		}
		announceAddrs = append(announceAddrs, fmt.Sprintf("%s/p2p/%s", addrStr, peerID))
	}

	// Include relay circuit addresses if available
	// These are addresses like /p2p/<relay-id>/p2p-circuit/p2p/<our-id>
	for _, conn := range p.host.Network().Conns() {
		if conn.Stat().Limited {
			// This is a relay connection — construct the relay circuit address
			relayAddr := conn.RemoteMultiaddr()
			relayPeer := conn.RemotePeer()
			circuitAddr := fmt.Sprintf("%s/p2p/%s/p2p-circuit/p2p/%s",
				relayAddr.String(), relayPeer.String(), peerID)
			announceAddrs = append(announceAddrs, circuitAddr)
		}
	}

	return announceAddrs
}

// listenPortFromAddrs extracts the first /tcp/<port> from the given multiaddrs.
// Used to build OS-derived announce addrs with the same port the host listens on.
// Returns empty string if no tcp port is found.
func listenPortFromAddrs(addrs []multiaddr.Multiaddr) string {
	for _, addr := range addrs {
		s := addr.String()
		if idx := strings.Index(s, "/tcp/"); idx != -1 {
			rest := s[idx+5:]
			if end := strings.Index(rest, "/"); end != -1 {
				return rest[:end]
			}
			return rest
		}
	}
	return ""
}

// addrsFromOS returns multiaddr strings for current network interface IPs (excluding loopback,
// link-local, and private/RFC1918 addresses), using the given listen port and peerID.
// Private IPs (10.x, 172.16-31.x, 192.168.x) from virtual adapters (Hyper-V, WSL, Docker)
// are not useful to remote peers and cause connection failures when advertised to the tracker.
// Caller is responsible for deduplication.
func addrsFromOS(listenPort, peerID string) []string {
	if listenPort == "" || peerID == "" {
		return nil
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var result []string
	for _, iface := range interfaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipNet.IP
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsPrivate() {
				continue
			}
			if ip4 := ip.To4(); ip4 != nil {
				result = append(result, fmt.Sprintf("/ip4/%s/tcp/%s/p2p/%s", ip4.String(), listenPort, peerID))
			} else if ip16 := ip.To16(); ip16 != nil {
				result = append(result, fmt.Sprintf("/ip6/%s/tcp/%s/p2p/%s", ip16.String(), listenPort, peerID))
			}
		}
	}
	return result
}

// AnnounceAddrsRefreshed returns multiaddrs for tracker registration, merging libp2p addrs (host + relay)
// with current OS network interface addrs so that new interfaces (e.g. VPN) appear within one heartbeat.
// Same filters as AnnounceAddrs (no loopback, no link-local). Deduplicated by full multiaddr string.
func (p *P2PHost) AnnounceAddrsRefreshed() []string {
	base := p.AnnounceAddrs()
	seen := make(map[string]bool, len(base))
	for _, a := range base {
		seen[a] = true
	}
	peerID := p.host.ID().String()
	rawAddrs := p.host.Addrs()
	port := listenPortFromAddrs(rawAddrs)
	if port == "" {
		return base
	}
	for _, a := range addrsFromOS(port, peerID) {
		if !seen[a] {
			seen[a] = true
			base = append(base, a)
		}
	}
	return base
}

// DHT returns the Kademlia DHT instance
func (p *P2PHost) DHT() *dht.IpfsDHT {
	return p.dht
}

// ProvideContent announces to the DHT that this peer provides the content identified by cidStr.
// BitTorrent-equivalent: announce_peer(infohash). Call after share/announce so other peers can find us via DHT.
// Runs in background; non-blocking. Safe to call with invalid CID (no-op, logged).
func (p *P2PHost) ProvideContent(ctx context.Context, cidStr string) error {
	if p.dht == nil {
		return nil
	}
	c, err := cid.Parse(cidStr)
	if err != nil {
		if p.logger != nil {
			p.logger.Debug("P2P", "ProvideContent", "CID parse failed, skipping DHT provide", map[string]interface{}{
				"cid": cidStr, "error": err.Error(),
			})
		}
		return nil
	}
	// brdcst=true so we announce to the network (BitTorrent-style)
	if err := p.dht.Provide(ctx, c, true); err != nil {
		if p.logger != nil {
			p.logger.Warn("P2P", "ProvideContent", "DHT Provide failed", map[string]interface{}{
				"cid": cidStr, "error": err.Error(),
			})
		}
		return err
	}
	if p.logger != nil {
		p.logger.Info("P2P", "ProvideContent", "DHT provide announced", map[string]interface{}{"cid": cidStr})
	}
	return nil
}

// FindProvidersForContent finds peers that provide the content identified by cidStr via the DHT.
// BitTorrent-equivalent: get_peers(infohash). Returns up to limit providers; limit 0 means use default (e.g. 20).
func (p *P2PHost) FindProvidersForContent(ctx context.Context, cidStr string, limit int) ([]peer.AddrInfo, error) {
	if p.dht == nil {
		return nil, nil
	}
	c, err := cid.Parse(cidStr)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	infos, err := p.dht.FindProviders(ctx, c)
	if err != nil {
		return nil, err
	}
	if len(infos) > limit {
		infos = infos[:limit]
	}
	return infos, nil
}

// PubSub returns the GossipSub instance
func (p *P2PHost) PubSub() *pubsub.PubSub {
	return p.pubsub
}

// MDNSEnabled returns true if mDNS is enabled
func (p *P2PHost) MDNSEnabled() bool {
	return p.mdnsService != nil
}

// ConnectedPeers returns the list of connected peer IDs
func (p *P2PHost) ConnectedPeers() []string {
	peers := p.host.Network().Peers()
	peerStrs := make([]string, len(peers))
	for i, p := range peers {
		peerStrs[i] = p.String()
	}
	return peerStrs
}

// SubscribeTopic subscribes to a GossipSub topic
func (p *P2PHost) SubscribeTopic(topic string) (*pubsub.Subscription, error) {
	p.subMutex.Lock()
	defer p.subMutex.Unlock()

	// Check if already subscribed
	if _, exists := p.subscriptions[topic]; exists {
		return nil, fmt.Errorf("already subscribed to topic %s", topic)
	}

	sub, err := p.pubsub.Subscribe(topic)
	if err != nil {
		return nil, fmt.Errorf("failed to subscribe to topic %s: %w", topic, err)
	}

	p.subscriptions[topic] = sub
	return sub, nil
}

// GetSubscriptions returns the list of subscribed topics
func (p *P2PHost) GetSubscriptions() []string {
	p.subMutex.RLock()
	defer p.subMutex.RUnlock()

	topics := make([]string, 0, len(p.subscriptions))
	for topic := range p.subscriptions {
		topics = append(topics, topic)
	}
	return topics
}

// ConnectToPeer connects to a specific peer using provided addrs, with DHT and relay circuit fallback.
// Security: relay circuit is only attempted through the trusted tracker relay (set at startup).
func (p *P2PHost) ConnectToPeer(peerID peer.ID, addrs []string) error {
	// Convert string addresses to multiaddr
	maddrs := make([]multiaddr.Multiaddr, 0, len(addrs))
	for _, addr := range addrs {
		maddr, err := multiaddr.NewMultiaddr(addr)
		if err != nil {
			continue
		}
		maddrs = append(maddrs, maddr)
	}

	peerInfo := peer.AddrInfo{
		ID:    peerID,
		Addrs: maddrs,
	}

	// Try direct connection first
	err := p.host.Connect(p.ctx, peerInfo)
	if err == nil {
		return nil
	}

	// Fallback 1: try finding the peer via DHT routing
	if p.dht != nil {
		ctx, cancel := context.WithTimeout(p.ctx, 15*time.Second)
		defer cancel()
		dhtInfo, dhtErr := p.dht.FindPeer(ctx, peerID)
		if dhtErr == nil && len(dhtInfo.Addrs) > 0 {
			if p.logger != nil {
				p.logger.Info("P2P", "ConnectToPeer", "Direct connect failed, trying DHT-discovered addrs", map[string]interface{}{
					"peer":      peerID.String(),
					"dht_addrs": len(dhtInfo.Addrs),
				})
			}
			if dhtConnErr := p.host.Connect(p.ctx, dhtInfo); dhtConnErr == nil {
				return nil
			}
		}
	}

	// Fallback 2: construct explicit relay circuit path through tracker relay.
	// Security: only uses the trusted tracker relay configured at startup — never arbitrary relays.
	if p.relayInfo != nil && len(p.relayInfo.Addrs) > 0 {
		if p.logger != nil {
			p.logger.Info("P2P", "ConnectToPeer", "Direct+DHT failed, attempting relay circuit via tracker relay", map[string]interface{}{
				"peer":     peerID.String(),
				"relay_id": p.relayInfo.ID.String(),
			})
		}

		// Clear dial backoff for relay peer — AutoRelay may have blacklisted it on startup
		if sw, ok := p.host.Network().(*swarm.Swarm); ok {
			sw.Backoff().Clear(p.relayInfo.ID)
		}

		// Pre-resolve DNS-based relay addrs to IPs (mobile carriers may fail DNS for ngrok)
		resolvedAddrs := resolveRelayAddrs(p.relayInfo.Addrs, p.logger)

		// First ensure we're connected to the relay itself (using resolved addrs)
		relayWithResolved := peer.AddrInfo{
			ID:    p.relayInfo.ID,
			Addrs: resolvedAddrs,
		}
		relayCtx, relayCancel := context.WithTimeout(p.ctx, 10*time.Second)
		defer relayCancel()
		if relayErr := p.host.Connect(relayCtx, relayWithResolved); relayErr != nil {
			if p.logger != nil {
				p.logger.Error("P2P", "ConnectToPeer", "Failed to connect to tracker relay", map[string]interface{}{
					"relay_id":       p.relayInfo.ID.String(),
					"error":          relayErr.Error(),
					"addrs_tried":    len(resolvedAddrs),
					"original_addrs": len(p.relayInfo.Addrs),
				})
			}
			return err // Return original direct-connect error
		}

		// Construct relay circuit multiaddr: <relay-addr>/p2p/<relay-id>/p2p-circuit/p2p/<target-id>
		circuitAddrs := make([]multiaddr.Multiaddr, 0, len(resolvedAddrs))
		for _, relayAddr := range resolvedAddrs {
			circuitStr := fmt.Sprintf("%s/p2p/%s/p2p-circuit/p2p/%s",
				relayAddr.String(), p.relayInfo.ID.String(), peerID.String())
			circuitMA, maErr := multiaddr.NewMultiaddr(circuitStr)
			if maErr != nil {
				continue
			}
			circuitAddrs = append(circuitAddrs, circuitMA)
		}

		if len(circuitAddrs) > 0 {
			// Clear dial backoff for target peer — direct dials put it in backoff
			if sw, ok := p.host.Network().(*swarm.Swarm); ok {
				sw.Backoff().Clear(peerID)
			}
			circuitInfo := peer.AddrInfo{
				ID:    peerID,
				Addrs: circuitAddrs,
			}
			circuitCtx, circuitCancel := context.WithTimeout(p.ctx, 30*time.Second)
			defer circuitCancel()
			if circuitErr := p.host.Connect(circuitCtx, circuitInfo); circuitErr == nil {
				if p.logger != nil {
					p.logger.Info("P2P", "ConnectToPeer", "Connected via relay circuit", map[string]interface{}{
						"peer":     peerID.String(),
						"relay_id": p.relayInfo.ID.String(),
					})
				}
				return nil
			} else if p.logger != nil {
				p.logger.Error("P2P", "ConnectToPeer", "Relay circuit dial failed", map[string]interface{}{
					"peer":  peerID.String(),
					"error": circuitErr.Error(),
				})
			}
		}
	}

	return err
}

// resolveRelayAddrs expands DNS-based multiaddrs to include IP-resolved versions.
// Mobile carriers sometimes fail DNS resolution for tunnel domains (e.g., ngrok).
// Returns the original addrs plus any resolved IP-based equivalents.
func resolveRelayAddrs(addrs []multiaddr.Multiaddr, log *logger.Logger) []multiaddr.Multiaddr {
	expanded := make([]multiaddr.Multiaddr, 0, len(addrs)*2)
	expanded = append(expanded, addrs...)

	for _, addr := range addrs {
		addrStr := addr.String()

		var ipProto, hostname, port string
		if strings.HasPrefix(addrStr, "/dns4/") {
			ipProto = "ip4"
		} else if strings.HasPrefix(addrStr, "/dns6/") {
			ipProto = "ip6"
		} else {
			continue
		}

		// Parse /dns4/<host>/tcp/<port>[/...]
		parts := strings.Split(addrStr, "/")
		if len(parts) < 5 || parts[3] != "tcp" {
			continue
		}
		hostname = parts[2]
		port = parts[4]
		suffix := ""
		if len(parts) > 5 {
			suffix = "/" + strings.Join(parts[5:], "/")
		}

		ips, err := net.LookupHost(hostname)
		if err != nil {
			if log != nil {
				log.Warn("P2P", "resolveRelayAddrs", "DNS resolution failed", map[string]interface{}{
					"hostname": hostname,
					"error":    err.Error(),
				})
			}
			continue
		}

		for _, ip := range ips {
			resolved := fmt.Sprintf("/%s/%s/tcp/%s%s", ipProto, ip, port, suffix)
			ma, maErr := multiaddr.NewMultiaddr(resolved)
			if maErr != nil {
				continue
			}
			expanded = append(expanded, ma)
			if log != nil {
				log.Info("P2P", "resolveRelayAddrs", "Resolved DNS to IP", map[string]interface{}{
					"hostname": hostname,
					"ip":       ip,
				})
			}
		}
	}

	return expanded
}

// Close shuts down the P2P host with a 30-second timeout
// Audit Item: F3 (P2P Shutdown Timeout)
// Acceptance Criterion: Uses context.WithTimeout(30s) to prevent hung shutdown
func (p *P2PHost) Close() error {
	if p.cancel != nil {
		p.cancel()
	}

	// Close block exchange service
	if p.blockExchangeSvc != nil {
		p.blockExchangeSvc.Close()
	}

	// Close mDNS
	if p.mdnsService != nil {
		p.mdnsService.Close()
	}

	// Close DHT
	if p.dht != nil {
		p.dht.Close()
	}

	// Close host with timeout protection
	if p.host != nil {
		// Create timeout context for shutdown
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		// Close host in goroutine with timeout
		done := make(chan error, 1)
		go func() {
			done <- p.host.Close()
		}()

		// Wait for either completion or timeout
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			return fmt.Errorf("P2P shutdown timed out after 30s")
		}
	}

	return nil
}

// RegisterBlockExchange registers the block exchange protocol handler
// This enables peer-to-peer chunk transfers for file sharing
func (p *P2PHost) RegisterBlockExchange(chunkProvider blockprotocol.ChunkProvider) error {
	if p.blockExchangeSvc != nil {
		return fmt.Errorf("block exchange already registered")
	}

	// Create block exchange service
	service := blockprotocol.NewBlockExchangeService()
	service.RegisterChunkProvider(chunkProvider)
	p.blockExchangeSvc = service

	// Register libp2p stream handler for block exchange protocol
	protocolID := service.ProtocolID()
	p.host.SetStreamHandler(protocolID, func(stream network.Stream) {
		// Get remote peer ID
		remotePeer := stream.Conn().RemotePeer()

		// Delegate to block exchange service
		if err := service.HandleStream(stream, remotePeer); err != nil {
			if p.logger != nil {
				p.logger.Error("[P2PHost.RegisterBlockExchange] Stream handler error", remotePeer.String(), err.Error(), nil)
			}
			stream.Reset()
			return
		}
	})

	if p.logger != nil {
		p.logger.Info("[P2PHost.RegisterBlockExchange] Block exchange protocol registered", string(protocolID), "", nil)
	}

	return nil
}

// BlockExchangeService returns the block exchange service
func (p *P2PHost) BlockExchangeService() blockprotocol.BlockExchangeService {
	return p.blockExchangeSvc
}

// RegisterAgentChatHandler registers the stream handler for direct token chat (holder → owner over libp2p).
// Owner's daemon uses this to respond to incoming agent-chat streams with local LLM.
func (p *P2PHost) RegisterAgentChatHandler(handler network.StreamHandler) {
	p.host.SetStreamHandler(blockprotocol.ProtocolIDAgentChat, handler)
}
