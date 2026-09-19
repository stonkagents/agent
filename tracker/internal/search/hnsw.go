// Package: tracker/internal/search
// Feature: F-002 (Intelligent Data Sharing)
// Story: US-002-02 (HNSW Vector Index for Fast Search)
// Purpose: Hierarchical Navigable Small World (HNSW) index for fast ANN search

package search

import (
	"context"
	"errors"
	"math"
	"math/rand"
	"sort"
	"sync"
)

// SearchResult represents a search result with CID and similarity score
type SearchResult struct {
	CID        string
	Similarity float32
}

// HNSWOptions configures the HNSW index parameters
type HNSWOptions struct {
	// Dimensions is the vector dimension (384 for all-MiniLM-L6-v2)
	Dimensions int

	// M is the max number of bi-directional links per node (typical: 12-48)
	// Higher M = better search quality but more memory
	M int

	// EfConstruct is the size of the dynamic candidate list during construction (typical: 100-300)
	// Higher efConstruct = better graph quality but slower insertion
	EfConstruct int

	// MaxLayers is the maximum number of hierarchical layers (typical: 5-10)
	MaxLayers int

	// Ml is the layer multiplier for level selection (typical: 1/ln(M))
	Ml float64
}

// HNSWIndex implements Hierarchical Navigable Small World graph for ANN search
type HNSWIndex struct {
	opts       HNSWOptions
	nodes      map[string]*node
	entryPoint string // CID of the entry point for search
	mu         sync.RWMutex
}

// node represents a vector in the HNSW graph
type node struct {
	cid       string
	vector    []float32
	layer     int              // Top layer this node belongs to
	neighbors map[int][]string // neighbors[layer] = list of CIDs
}

// NewHNSWIndex creates a new HNSW index for approximate nearest neighbor search
func NewHNSWIndex(opts HNSWOptions) *HNSWIndex {
	// Set defaults if not provided
	if opts.Dimensions == 0 {
		opts.Dimensions = 384
	}
	if opts.M == 0 {
		opts.M = 16
	}
	if opts.EfConstruct == 0 {
		opts.EfConstruct = 200
	}
	if opts.MaxLayers == 0 {
		opts.MaxLayers = 5
	}
	if opts.Ml == 0 {
		opts.Ml = 1.0 / math.Log(float64(opts.M))
	}

	return &HNSWIndex{
		opts:  opts,
		nodes: make(map[string]*node),
	}
}

// Insert adds a vector to the HNSW index
func (h *HNSWIndex) Insert(ctx context.Context, cid string, vector []float32) error {
	if len(vector) != h.opts.Dimensions {
		return errors.New("vector dimension mismatch")
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	// Check for duplicates
	if _, exists := h.nodes[cid]; exists {
		return errors.New("CID already exists in index")
	}

	// Select layer for new node (exponential decay probability)
	layer := h.selectLayer()

	// Create new node
	newNode := &node{
		cid:       cid,
		vector:    vector,
		layer:     layer,
		neighbors: make(map[int][]string),
	}

	// If this is the first node, make it the entry point
	if h.entryPoint == "" {
		h.entryPoint = cid
		h.nodes[cid] = newNode
		return nil
	}

	// Find nearest neighbors at each layer and connect
	currentNearest := h.entryPoint
	for lc := h.nodes[h.entryPoint].layer; lc >= 0; lc-- {
		// Search for nearest neighbors at this layer
		candidates := h.searchLayerInternal(vector, currentNearest, h.opts.EfConstruct, lc)

		// For layers where the new node exists, connect bidirectionally
		if lc <= layer {
			// Select M nearest neighbors
			neighbors := h.selectNeighbors(candidates, h.opts.M)

			// Add bidirectional links
			for _, neighborCID := range neighbors {
				// Add neighbor to new node
				newNode.neighbors[lc] = append(newNode.neighbors[lc], neighborCID)

				// Add new node to neighbor (bidirectional)
				neighbor := h.nodes[neighborCID]
				neighbor.neighbors[lc] = append(neighbor.neighbors[lc], cid)

				// Prune neighbor connections if exceeds M
				if len(neighbor.neighbors[lc]) > h.opts.M {
					neighbor.neighbors[lc] = h.pruneNeighbors(neighbor.vector, neighbor.neighbors[lc], h.opts.M)
				}
			}
		}

		// Update current nearest for next layer
		if len(candidates) > 0 {
			currentNearest = candidates[0].CID
		}
	}

	// Add node to index
	h.nodes[cid] = newNode

	// Update entry point if new node is at a higher layer
	if layer > h.nodes[h.entryPoint].layer {
		h.entryPoint = cid
	}

	return nil
}

// Search performs k-nearest neighbor search
func (h *HNSWIndex) Search(ctx context.Context, query []float32, k int) ([]*SearchResult, error) {
	if len(query) != h.opts.Dimensions {
		return nil, errors.New("query vector dimension mismatch")
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.entryPoint == "" {
		return []*SearchResult{}, nil // Empty index
	}

	// Start from entry point and search down through layers
	currentNearest := h.entryPoint
	for lc := h.nodes[h.entryPoint].layer; lc > 0; lc-- {
		candidates := h.searchLayerInternal(query, currentNearest, 1, lc)
		if len(candidates) > 0 {
			currentNearest = candidates[0].CID
		}
	}

	// Search at layer 0 with higher ef for better recall
	ef := max(h.opts.EfConstruct, k)
	candidates := h.searchLayerInternal(query, currentNearest, ef, 0)

	// Return top k results
	if len(candidates) > k {
		candidates = candidates[:k]
	}

	return candidates, nil
}

// searchLayerInternal performs greedy search at a specific layer
func (h *HNSWIndex) searchLayerInternal(query []float32, entryPoint string, ef int, layer int) []*SearchResult {
	visited := make(map[string]bool)
	candidates := make([]*SearchResult, 0, ef)
	w := make([]*SearchResult, 0, ef) // Dynamic candidate list

	// Start with entry point
	dist := cosineSimilarity(query, h.nodes[entryPoint].vector)
	candidates = append(candidates, &SearchResult{CID: entryPoint, Similarity: dist})
	w = append(w, &SearchResult{CID: entryPoint, Similarity: dist})
	visited[entryPoint] = true

	// Greedy search
	for len(candidates) > 0 {
		// Get closest candidate
		sort.Slice(candidates, func(i, j int) bool {
			return candidates[i].Similarity > candidates[j].Similarity
		})
		current := candidates[0]
		candidates = candidates[1:]

		// Check if we should stop (current is farther than furthest in w)
		sort.Slice(w, func(i, j int) bool {
			return w[i].Similarity > w[j].Similarity
		})
		if len(w) >= ef && current.Similarity < w[len(w)-1].Similarity {
			break
		}

		// Explore neighbors
		currentNode := h.nodes[current.CID]
		if currentNode.neighbors[layer] == nil {
			continue
		}

		for _, neighborCID := range currentNode.neighbors[layer] {
			if visited[neighborCID] {
				continue
			}
			visited[neighborCID] = true

			neighborDist := cosineSimilarity(query, h.nodes[neighborCID].vector)
			if len(w) < ef || neighborDist > w[len(w)-1].Similarity {
				candidates = append(candidates, &SearchResult{CID: neighborCID, Similarity: neighborDist})
				w = append(w, &SearchResult{CID: neighborCID, Similarity: neighborDist})

				// Keep w size bounded to ef
				if len(w) > ef {
					sort.Slice(w, func(i, j int) bool {
						return w[i].Similarity > w[j].Similarity
					})
					w = w[:ef]
				}
			}
		}
	}

	// Return top ef results sorted by similarity
	sort.Slice(w, func(i, j int) bool {
		return w[i].Similarity > w[j].Similarity
	})

	return w
}

// selectLayer chooses a random layer for a new node (exponential decay)
func (h *HNSWIndex) selectLayer() int {
	// Random level with exponential decay probability
	r := rand.Float64()
	layer := int(-math.Log(r) * h.opts.Ml)
	if layer > h.opts.MaxLayers {
		layer = h.opts.MaxLayers
	}
	return layer
}

// selectNeighbors selects M best neighbors from candidates
func (h *HNSWIndex) selectNeighbors(candidates []*SearchResult, m int) []string {
	// Sort by similarity (descending)
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Similarity > candidates[j].Similarity
	})

	// Select top M
	neighbors := make([]string, 0, m)
	for i := 0; i < len(candidates) && i < m; i++ {
		neighbors = append(neighbors, candidates[i].CID)
	}

	return neighbors
}

// pruneNeighbors reduces neighbor list to M by selecting most similar
func (h *HNSWIndex) pruneNeighbors(vector []float32, neighbors []string, m int) []string {
	if len(neighbors) <= m {
		return neighbors
	}

	// Calculate similarities (skip nodes that don't exist yet)
	candidates := make([]*SearchResult, 0, len(neighbors))
	for _, neighborCID := range neighbors {
		neighborNode := h.nodes[neighborCID]
		if neighborNode == nil {
			continue // Skip if node doesn't exist yet
		}
		similarity := cosineSimilarity(vector, neighborNode.vector)
		candidates = append(candidates, &SearchResult{CID: neighborCID, Similarity: similarity})
	}

	// Select top M
	return h.selectNeighbors(candidates, m)
}

// cosineSimilarity calculates cosine similarity between two vectors
// Returns value in [-1, 1] where 1 = identical, 0 = orthogonal, -1 = opposite
func cosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) {
		return 0
	}

	var dotProduct float32
	var normA float32
	var normB float32

	for i := 0; i < len(a); i++ {
		dotProduct += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}

	if normA == 0 || normB == 0 {
		return 0
	}

	return dotProduct / (float32(math.Sqrt(float64(normA))) * float32(math.Sqrt(float64(normB))))
}

// Size returns the number of vectors in the index
func (h *HNSWIndex) Size() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.nodes)
}
