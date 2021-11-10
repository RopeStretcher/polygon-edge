package protocol

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/0xPolygon/polygon-sdk/blockchain"
	"github.com/0xPolygon/polygon-sdk/helper/tests"
	"github.com/0xPolygon/polygon-sdk/network"
	"github.com/0xPolygon/polygon-sdk/types"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/assert"
)

func TestHandleNewPeer(t *testing.T) {
	tests := []struct {
		name       string
		chain      blockchainShim
		peerChains []blockchainShim
	}{
		{
			name:  "should set peer's status",
			chain: NewRandomChain(t, 5),
			peerChains: []blockchainShim{
				NewRandomChain(t, 5),
				NewRandomChain(t, 10),
				NewRandomChain(t, 15),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			syncer, peerSyncers := SetupSyncerNetwork(t, tt.chain, tt.peerChains)

			// Check peer's status in Syncer's peer list
			for _, peerSyncer := range peerSyncers {
				peer := getPeer(syncer, peerSyncer.server.AddrInfo().ID)
				assert.NotNil(t, peer, "syncer must have peer's status, but nil")

				// should receive latest status
				expectedStatus := GetCurrentStatus(peerSyncer.blockchain)
				assert.Equal(t, expectedStatus, peer.status)
			}
		})
	}
}

func TestDeletePeer(t *testing.T) {
	tests := []struct {
		name                 string
		chain                blockchainShim
		peerChains           []blockchainShim
		numDisconnectedPeers int
	}{
		{
			name:  "should not have data in peers for disconnected peer",
			chain: NewRandomChain(t, 5),
			peerChains: []blockchainShim{
				NewRandomChain(t, 5),
				NewRandomChain(t, 10),
				NewRandomChain(t, 15),
			},
			numDisconnectedPeers: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			syncer, peerSyncers := SetupSyncerNetwork(t, tt.chain, tt.peerChains)

			// disconnects from syncer
			for i := 0; i < tt.numDisconnectedPeers; i++ {
				peerSyncers[i].server.Disconnect(syncer.server.AddrInfo().ID, "bye")
			}
			WaitUntilPeerConnected(t, syncer, len(tt.peerChains)-tt.numDisconnectedPeers, 10*time.Second)

			for idx, peerSyncer := range peerSyncers {
				shouldBeDeleted := idx < tt.numDisconnectedPeers
				peer := getPeer(syncer, peerSyncer.server.AddrInfo().ID)
				if shouldBeDeleted {
					assert.Nil(t, peer)
				} else {
					assert.NotNil(t, peer)
				}
			}
		})
	}
}

func TestBroadcast(t *testing.T) {
	tests := []struct {
		name         string
		chain        blockchainShim
		peerChain    blockchainShim
		numNewBlocks int
	}{
		{
			name:         "syncer should receive new block in peer",
			chain:        NewRandomChain(t, 5),
			peerChain:    NewRandomChain(t, 10),
			numNewBlocks: 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			syncer, peerSyncers := SetupSyncerNetwork(t, tt.chain, []blockchainShim{tt.peerChain})
			peerSyncer := peerSyncers[0]

			newBlocks := GenerateNewBlocks(t, peerSyncer.blockchain, tt.numNewBlocks)
			for _, newBlock := range newBlocks {
				peerSyncer.Broadcast(newBlock)
			}

			peer := getPeer(syncer, peerSyncer.server.AddrInfo().ID)
			assert.NotNil(t, peer)

			// Check peer's queue
			assert.Len(t, peer.enqueue, tt.numNewBlocks)
			for _, newBlock := range newBlocks {
				block, ok := TryPopBlock(t, syncer, peerSyncer.server.AddrInfo().ID, 10*time.Second)
				assert.True(t, ok, "syncer should be able to pop new block from peer %s", peerSyncer.server.AddrInfo().ID)
				assert.Equal(t, newBlock, block, "syncer should get the same block peer broadcasted")
			}

			// Check peer's status
			lastBlock := newBlocks[len(newBlocks)-1]
			assert.Equal(t, HeaderToStatus(lastBlock.Header), peer.status)
		})
	}
}

func TestBestPeer(t *testing.T) {
	tests := []struct {
		name          string
		chain         blockchainShim
		peersChain    []blockchainShim
		found         bool
		bestPeerIndex int
	}{
		{
			name:  "should find the peer that has the longest chain",
			chain: NewRandomChain(t, 100),
			peersChain: []blockchainShim{
				NewRandomChain(t, 10),
				NewRandomChain(t, 1000),
				NewRandomChain(t, 100),
				NewRandomChain(t, 10),
			},
			found:         true,
			bestPeerIndex: 1,
		},
		{
			name:  "shouldn't find if all peer doesn't have longer chain than syncer's chain",
			chain: NewRandomChain(t, 1000),
			peersChain: []blockchainShim{
				NewRandomChain(t, 10),
				NewRandomChain(t, 10),
				NewRandomChain(t, 10),
			},
			found: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			syncer, peerSyncers := SetupSyncerNetwork(t, tt.chain, tt.peersChain)

			bestPeer := syncer.BestPeer()
			if tt.found {
				assert.NotNil(t, bestPeer, "syncer should find best peer, but not found")

				expectedBestPeer := peerSyncers[tt.bestPeerIndex]
				expectedBestPeerStatus := GetCurrentStatus(expectedBestPeer.blockchain)
				assert.Equal(t, expectedBestPeer.server.AddrInfo().ID.String(), bestPeer.peer.String())
				assert.Equal(t, expectedBestPeerStatus, bestPeer.status)
			} else {
				assert.Nil(t, bestPeer, "syncer shouldn't find best peer, but found")
			}
		})
	}
}

func TestFindCommonAncestor(t *testing.T) {
	tests := []struct {
		name          string
		syncerHeaders []*types.Header
		peerHeaders   []*types.Header
		// result
		found       bool
		headerIndex int
		forkIndex   int
		err         error
	}{
		{
			name:          "should find common ancestor",
			syncerHeaders: blockchain.NewTestHeaderChainWithSeed(nil, 10, 0),
			peerHeaders:   blockchain.NewTestHeaderChainWithSeed(nil, 20, 0),
			found:         true,
			headerIndex:   9,
			forkIndex:     10,
			err:           nil,
		},
		{
			name:          "should return error if there is no fork",
			syncerHeaders: blockchain.NewTestHeaderChainWithSeed(nil, 11, 0),
			peerHeaders:   blockchain.NewTestHeaderChainWithSeed(nil, 10, 0),
			found:         false,
			err:           errors.New("fork not found"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chain, peerChain := blockchain.NewTestBlockchain(t, tt.syncerHeaders), blockchain.NewTestBlockchain(t, tt.peerHeaders)
			syncer, peerSyncers := SetupSyncerNetwork(t, chain, []blockchainShim{peerChain})
			peerSyncer := peerSyncers[0]

			peer := getPeer(syncer, peerSyncer.server.AddrInfo().ID)
			assert.NotNil(t, peer)

			header, fork, err := syncer.findCommonAncestor(peer.client, peer.status)
			if tt.found {
				assert.Equal(t, tt.peerHeaders[tt.headerIndex], header)
				assert.Equal(t, tt.peerHeaders[tt.forkIndex], fork)
				assert.Nil(t, err)
			} else {
				assert.Nil(t, header)
				assert.Nil(t, fork)
				assert.Equal(t, tt.err, err)
			}
		})
	}
}

func TestWatchSyncWithPeer(t *testing.T) {
	tests := []struct {
		name           string
		headers        []*types.Header
		peerHeaders    []*types.Header
		numNewBlocks   int
		synced         bool
		expectedHeight uint64
	}{
		{
			name:           "should sync until peer's latest block",
			headers:        blockchain.NewTestHeaderChainWithSeed(nil, 10, 0),
			peerHeaders:    blockchain.NewTestHeaderChainWithSeed(nil, 1, 0),
			numNewBlocks:   15,
			synced:         true,
			expectedHeight: 15,
		},
		{
			name:           "shouldn't sync",
			headers:        blockchain.NewTestHeaderChainWithSeed(nil, 10, 0),
			peerHeaders:    blockchain.NewTestHeaderChainWithSeed(nil, 1, 0),
			numNewBlocks:   9,
			synced:         false,
			expectedHeight: 9,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chain, peerChain := NewMockBlockchain(tt.headers), NewMockBlockchain(tt.peerHeaders)
			syncer, peerSyncers := SetupSyncerNetwork(t, chain, []blockchainShim{peerChain})
			peerSyncer := peerSyncers[0]

			newBlocks := GenerateNewBlocks(t, peerChain, tt.numNewBlocks)
			for _, b := range newBlocks {
				peerSyncer.Broadcast(b)
			}

			peer := getPeer(syncer, peerSyncer.server.AddrInfo().ID)
			assert.NotNil(t, peer)

			latestBlock := newBlocks[len(newBlocks)-1]
			doneCh := make(chan struct{}, 1)
			go func() {
				syncer.WatchSyncWithPeer(peer, func(b *types.Block) bool {
					// sync until latest block
					return b.Header.Number >= latestBlock.Header.Number
				})
				// wait until syncer updates status by latest block
				WaitUntilProcessedAllEvents(t, syncer, 10*time.Second)
				doneCh <- struct{}{}
			}()

			select {
			case <-doneCh:
				assert.True(t, tt.synced, "syncer shouldn't sync any block with peer, but did")
				assert.Equal(t, HeaderToStatus(latestBlock.Header), syncer.status)
				assert.Equal(t, tt.expectedHeight, syncer.status.Number)
				break
			case <-time.After(time.Second * 10):
				assert.False(t, tt.synced, "syncer should sync blocks with peer, but didn't")
				break
			}
		})
	}
}

func TestBulkSyncWithPeer(t *testing.T) {
	tests := []struct {
		name        string
		headers     []*types.Header
		peerHeaders []*types.Header
		// result
		synced bool
		err    error
	}{
		{
			name:        "should sync until peer's latest block",
			headers:     blockchain.NewTestHeaderChainWithSeed(nil, 10, 0),
			peerHeaders: blockchain.NewTestHeaderChainWithSeed(nil, 20, 0),
			synced:      true,
			err:         nil,
		},
		{
			name:        "should sync until peer's latest block",
			headers:     blockchain.NewTestHeaderChainWithSeed(nil, 20, 0),
			peerHeaders: blockchain.NewTestHeaderChainWithSeed(nil, 10, 0),
			synced:      false,
			err:         errors.New("fork not found"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chain, peerChain := NewMockBlockchain(tt.headers), NewMockBlockchain(tt.peerHeaders)
			syncer, peerSyncers := SetupSyncerNetwork(t, chain, []blockchainShim{peerChain})
			peerSyncer := peerSyncers[0]

			peer := getPeer(syncer, peerSyncer.server.AddrInfo().ID)
			assert.NotNil(t, peer)

			err := syncer.BulkSyncWithPeer(peer)
			assert.Equal(t, tt.err, err)
			WaitUntilProcessedAllEvents(t, syncer, 10*time.Second)

			expectedStatus := HeaderToStatus(tt.headers[len(tt.headers)-1])
			if tt.synced {
				expectedStatus = HeaderToStatus(tt.peerHeaders[len(tt.peerHeaders)-1])
			}
			assert.Equal(t, expectedStatus, syncer.status)
		})
	}
}

type mockBlockStore struct {
	blocks       []*types.Block
	subscription *blockchain.MockSubscription
	td           *big.Int
}

func newMockBlockStore() *mockBlockStore {
	bs := &mockBlockStore{
		blocks:       make([]*types.Block, 0),
		subscription: blockchain.NewMockSubscription(),
		td:           big.NewInt(1),
	}
	return bs
}

func (m *mockBlockStore) Header() *types.Header {
	return m.blocks[len(m.blocks)-1].Header
}
func (m *mockBlockStore) GetHeaderByNumber(n uint64) (*types.Header, bool) {
	b, ok := m.GetBlockByNumber(n, false)
	if !ok {
		return nil, false
	}
	return b.Header, true
}
func (m *mockBlockStore) GetBlockByNumber(blockNumber uint64, full bool) (*types.Block, bool) {
	for _, b := range m.blocks {
		if b.Number() == blockNumber {
			return b, true
		}
	}
	return nil, false
}
func (m *mockBlockStore) SubscribeEvents() blockchain.Subscription {
	return m.subscription
}
func (m *mockBlockStore) GetReceiptsByHash(types.Hash) ([]*types.Receipt, error) {
	return nil, nil
}

func (m *mockBlockStore) GetHeaderByHash(hash types.Hash) (*types.Header, bool) {
	for _, b := range m.blocks {
		header := b.Header.ComputeHash()
		if header.Hash == hash {
			return header, true
		}
	}
	return nil, true
}
func (m *mockBlockStore) GetBodyByHash(hash types.Hash) (*types.Body, bool) {
	for _, b := range m.blocks {

		if b.Hash() == hash {
			return b.Body(), true
		}
	}
	return nil, true
}
func (m *mockBlockStore) WriteBlocks(blocks []*types.Block) error {

	for _, b := range blocks {
		m.td.Add(m.td, big.NewInt(int64(b.Header.Difficulty)))
		m.blocks = append(m.blocks, b)
	}
	return nil
}

func (m *mockBlockStore) CurrentTD() *big.Int {
	return m.td
}

func (m *mockBlockStore) GetTD(hash types.Hash) (*big.Int, bool) {
	return m.td, false
}
func createGenesisBlock() []*types.Block {
	blocks := make([]*types.Block, 0)
	genesis := &types.Header{Difficulty: 1, Number: 0}
	genesis.ComputeHash()
	b := &types.Block{
		Header: genesis,
	}
	blocks = append(blocks, b)
	return blocks
}

func createBlockStores(count int) (bStore []*mockBlockStore) {
	bStore = make([]*mockBlockStore, count)
	for i := 0; i < count; i++ {
		bStore[i] = newMockBlockStore()
	}
	return
}

// createNetworkServers is a helper function for generating network servers
func createNetworkServers(count int, t *testing.T, conf func(c *network.Config)) []*network.Server {
	networkServers := make([]*network.Server, count)

	for indx := 0; indx < count; indx++ {
		networkServers[indx] = network.CreateServer(t, conf)
	}

	return networkServers
}

// createSyncers is a helper function for generating syncers. Servers and BlockStores should be at least the length
// of count
func createSyncers(count int, servers []*network.Server, blockStores []*mockBlockStore) []*Syncer {
	syncers := make([]*Syncer, count)

	for indx := 0; indx < count; indx++ {
		syncers[indx] = NewSyncer(hclog.NewNullLogger(), servers[indx], blockStores[indx])
	}

	return syncers
}

// numSyncPeers returns the number of sync peers
func numSyncPeers(syncer *Syncer) int64 {
	num := 0
	syncer.peers.Range(func(key, value interface{}) bool {
		num++

		return true
	})

	return int64(num)
}

// WaitUntilSyncPeersNumber waits until the number of sync peers reaches a certain number, otherwise it times out
func WaitUntilSyncPeersNumber(ctx context.Context, syncer *Syncer, requiredNum int64) (int64, error) {
	res, err := tests.RetryUntilTimeout(ctx, func() (interface{}, bool) {
		numPeers := numSyncPeers(syncer)
		if numPeers == requiredNum {
			return numPeers, false
		}
		return nil, true
	})

	if err != nil {
		return 0, err
	}
	return res.(int64), nil
}

func TestSyncer_PeerDisconnected(t *testing.T) {
	conf := func(c *network.Config) {
		c.MaxPeers = 4
		c.NoDiscover = true
	}
	blocks := createGenesisBlock()

	// Create three servers
	servers := createNetworkServers(3, t, conf)

	// Create the block stores
	blockStores := createBlockStores(3)

	for _, blockStore := range blockStores {
		assert.NoError(t, blockStore.WriteBlocks(blocks))
	}

	// Create the syncers
	syncers := createSyncers(3, servers, blockStores)

	// Start the syncers
	for _, syncer := range syncers {
		go syncer.Start()
	}

	network.MultiJoin(
		t,
		servers[0],
		servers[1],
		servers[0],
		servers[2],
		servers[1],
		servers[2],
	)

	// wait until gossip protocol builds the mesh network (https://github.com/libp2p/specs/blob/master/pubsub/gossipsub/gossipsub-v1.0.md)
	waitCtx, cancelWait := context.WithTimeout(context.Background(), time.Second*10)
	defer cancelWait()

	numPeers, err := WaitUntilSyncPeersNumber(waitCtx, syncers[1], 2)
	if err != nil {
		t.Fatalf("Unable to add sync peers, %v", err)
	}
	// Make sure the number of peers is correct
	// -1 to exclude the current node
	assert.Equal(t, int64(len(servers)-1), numPeers)

	// Disconnect peer2
	peerToDisconnect := servers[2].AddrInfo().ID
	servers[1].Disconnect(peerToDisconnect, "testing")

	waitCtx, cancelWait = context.WithTimeout(context.Background(), time.Second*10)
	defer cancelWait()
	numPeers, err = WaitUntilSyncPeersNumber(waitCtx, syncers[1], 1)
	if err != nil {
		t.Fatalf("Unable to disconnect sync peers, %v", err)
	}
	// Make sure a single peer disconnected
	// Additional -1 to exclude the current node
	assert.Equal(t, int64(len(servers)-2), numPeers)

	// server1 syncer should have disconnected from server2 peer
	_, found := syncers[1].peers.Load(peerToDisconnect)

	// Make sure that the disconnected peer is not in the
	// reference node's sync peer map
	assert.False(t, found)
}
