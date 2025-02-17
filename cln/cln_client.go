package cln

import (
	"encoding/hex"
	"fmt"
	"log"
	"path/filepath"

	"github.com/breez/lspd/basetypes"
	"github.com/breez/lspd/lightning"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/niftynei/glightning/glightning"
	"golang.org/x/exp/slices"
)

type ClnClient struct {
	client *glightning.Lightning
}

var (
	OPEN_STATUSES    = []string{"CHANNELD_NORMAL"}
	PENDING_STATUSES = []string{"OPENINGD", "CHANNELD_AWAITING_LOCKIN"}
	CLOSING_STATUSES = []string{"CHANNELD_SHUTTING_DOWN", "CLOSINGD_SIGEXCHANGE", "CLOSINGD_COMPLETE", "AWAITING_UNILATERAL", "FUNDING_SPEND_SEEN", "ONCHAIN"}
	CLOSED_STATUSES  = []string{"CLOSED"}
)

func NewClnClient(socketPath string) (*ClnClient, error) {
	rpcFile := filepath.Base(socketPath)
	if rpcFile == "" || rpcFile == "." {
		return nil, fmt.Errorf("invalid socketPath '%s'", socketPath)
	}
	lightningDir := filepath.Dir(socketPath)
	if lightningDir == "" || lightningDir == "." {
		return nil, fmt.Errorf("invalid socketPath '%s'", socketPath)
	}

	client := glightning.NewLightning()
	client.SetTimeout(60)
	client.StartUp(rpcFile, lightningDir)
	return &ClnClient{
		client: client,
	}, nil
}

func (c *ClnClient) GetInfo() (*lightning.GetInfoResult, error) {
	info, err := c.client.GetInfo()
	if err != nil {
		log.Printf("CLN: client.GetInfo() error: %v", err)
		return nil, err
	}

	return &lightning.GetInfoResult{
		Alias:  info.Alias,
		Pubkey: info.Id,
	}, nil
}

func (c *ClnClient) IsConnected(destination []byte) (bool, error) {
	pubKey := hex.EncodeToString(destination)
	peers, err := c.client.ListPeers()
	if err != nil {
		log.Printf("CLN: client.ListPeers() error: %v", err)
		return false, fmt.Errorf("CLN: client.ListPeers() error: %w", err)
	}

	for _, peer := range peers {
		if pubKey == peer.Id {
			log.Printf("destination online: %x", destination)
			return true, nil
		}
	}

	log.Printf("CLN: destination offline: %x", destination)
	return false, nil
}

func (c *ClnClient) OpenChannel(req *lightning.OpenChannelRequest) (*wire.OutPoint, error) {
	pubkey := hex.EncodeToString(req.Destination)
	var minConfs *uint16
	if req.MinConfs != nil {
		m := uint16(*req.MinConfs)
		minConfs = &m
	}
	var minDepth *uint16
	if req.IsZeroConf {
		var d uint16 = 0
		minDepth = &d
	}

	var rate *glightning.FeeRate
	if req.FeeSatPerVByte != nil {
		rate = &glightning.FeeRate{
			Rate:  uint(*req.FeeSatPerVByte * 1000),
			Style: glightning.PerKb,
		}
	} else if req.TargetConf != nil {
		if *req.TargetConf < 3 {
			rate = &glightning.FeeRate{
				Directive: glightning.Urgent,
			}
		} else if *req.TargetConf < 30 {
			rate = &glightning.FeeRate{
				Directive: glightning.Normal,
			}
		} else {
			rate = &glightning.FeeRate{
				Directive: glightning.Slow,
			}
		}
	}

	fundResult, err := c.client.FundChannelExt(
		pubkey,
		glightning.NewSat(int(req.CapacitySat)),
		rate,
		!req.IsPrivate,
		minConfs,
		glightning.NewMsat(0),
		minDepth,
		glightning.NewSat(0),
	)

	if err != nil {
		log.Printf("CLN: client.FundChannelExt(%v, %v) error: %v", pubkey, req.CapacitySat, err)
		return nil, err
	}

	fundingTxId, err := chainhash.NewHashFromStr(fundResult.FundingTxId)
	if err != nil {
		log.Printf("CLN: chainhash.NewHashFromStr(%s) error: %v", fundResult.FundingTxId, err)
		return nil, err
	}

	channelPoint, err := basetypes.NewOutPoint(fundingTxId[:], uint32(fundResult.FundingTxOutputNum))
	if err != nil {
		log.Printf("CLN: NewOutPoint(%s, %d) error: %v", fundingTxId.String(), fundResult.FundingTxOutputNum, err)
		return nil, err
	}

	return channelPoint, nil
}

func (c *ClnClient) GetChannel(peerID []byte, channelPoint wire.OutPoint) (*lightning.GetChannelResult, error) {
	pubkey := hex.EncodeToString(peerID)
	peer, err := c.client.GetPeer(pubkey)
	if err != nil {
		log.Printf("CLN: client.GetPeer(%s) error: %v", pubkey, err)
		return nil, err
	}

	fundingTxID := channelPoint.Hash.String()
	for _, c := range peer.Channels {
		log.Printf("getChannel destination: %s, Short channel id: %v, local alias: %v , FundingTxID:%v, State:%v ", pubkey, c.ShortChannelId, c.Alias.Local, c.FundingTxId, c.State)
		if slices.Contains(OPEN_STATUSES, c.State) && c.FundingTxId == fundingTxID {
			confirmedChanID, err := basetypes.NewShortChannelIDFromString(c.ShortChannelId)
			if err != nil {
				fmt.Printf("NewShortChannelIDFromString %v error: %v", c.ShortChannelId, err)
				return nil, err
			}
			initialChanID, err := basetypes.NewShortChannelIDFromString(c.Alias.Local)
			if err != nil {
				fmt.Printf("NewShortChannelIDFromString %v error: %v", c.Alias.Local, err)
				return nil, err
			}
			return &lightning.GetChannelResult{
				InitialChannelID:   *initialChanID,
				ConfirmedChannelID: *confirmedChanID,
			}, nil
		}
	}

	log.Printf("No channel found: getChannel(%v, %v)", pubkey, fundingTxID)
	return nil, fmt.Errorf("no channel found")
}

func (c *ClnClient) GetNodeChannelCount(nodeID []byte) (int, error) {
	pubkey := hex.EncodeToString(nodeID)
	peer, err := c.client.GetPeer(pubkey)
	if err != nil {
		log.Printf("CLN: client.GetPeer(%s) error: %v", pubkey, err)
		return 0, err
	}

	count := 0
	openPendingStatuses := append(OPEN_STATUSES, PENDING_STATUSES...)
	for _, c := range peer.Channels {
		if slices.Contains(openPendingStatuses, c.State) {
			count++
		}
	}

	return count, nil
}

func (c *ClnClient) GetClosedChannels(nodeID string, channelPoints map[string]uint64) (map[string]uint64, error) {
	r := make(map[string]uint64)
	if len(channelPoints) == 0 {
		return r, nil
	}

	peer, err := c.client.GetPeer(nodeID)
	if err != nil {
		log.Printf("CLN: client.GetPeer(%s) error: %v", nodeID, err)
		return nil, err
	}

	lookup := make(map[string]uint64)
	for _, c := range peer.Channels {
		if slices.Contains(CLOSING_STATUSES, c.State) {
			cid, err := basetypes.NewShortChannelIDFromString(c.ShortChannelId)
			if err != nil {
				log.Printf("CLN: GetClosedChannels NewShortChannelIDFromString(%v) error: %v", c.ShortChannelId, err)
				continue
			}

			outnum := uint64(*cid) & 0xFFFFFF
			cp := fmt.Sprintf("%s:%d", c.FundingTxId, outnum)
			lookup[cp] = uint64(*cid)
		}
	}

	for c, h := range channelPoints {
		if _, ok := lookup[c]; !ok {
			r[c] = h
		}
	}

	return r, nil
}
