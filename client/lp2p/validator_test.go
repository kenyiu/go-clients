package lp2p

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/drand/drand/common"
	"github.com/drand/drand/common/key"
	"testing"
	"time"

	clock "github.com/jonboulle/clockwork"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	pb "github.com/libp2p/go-libp2p-pubsub/pb"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"google.golang.org/protobuf/proto"

	"github.com/drand/drand-cli/client"
	"github.com/drand/drand-cli/client/test/cache"
	chain2 "github.com/drand/drand/common/chain"
	"github.com/drand/drand/common/testlogger"
	dcrypto "github.com/drand/drand/crypto"
	"github.com/drand/drand/protobuf/drand"
)

type randomDataWrapper struct {
	data client.RandomData
}

func (r *randomDataWrapper) GetRound() uint64 {
	return r.data.Rnd
}

func (r *randomDataWrapper) GetSignature() []byte {
	return r.data.Sig
}

func (r *randomDataWrapper) GetRandomness() []byte {
	return r.data.Random
}

func randomPeerID(t *testing.T) peer.ID {
	priv, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	peerID, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return peerID
}

func fakeRandomData(info *chain2.Info, clk clock.Clock) client.RandomData {
	rnd := common.CurrentRound(clk.Now().Unix(), info.Period, info.GenesisTime)

	sig := make([]byte, 8)
	binary.LittleEndian.PutUint64(sig, rnd)
	psig := make([]byte, 8)
	binary.LittleEndian.PutUint64(psig, rnd-1)

	return client.RandomData{
		Rnd:               rnd,
		Sig:               sig,
		PreviousSignature: psig,
		Random:            dcrypto.RandomnessFromSignature(sig),
	}
}

func fakeChainInfo() *chain2.Info {
	sch, err := dcrypto.GetSchemeFromEnv()
	if err != nil {
		panic(err)
	}
	pair, err := key.NewKeyPair("fakeChainInfo.test:1234", sch)
	if err != nil {
		panic(err)
	}

	return &chain2.Info{
		Period:      time.Second,
		GenesisTime: time.Now().Unix(),
		PublicKey:   pair.Public.Key,
		Scheme:      sch.Name,
	}
}

func TestRejectsUnmarshalBeaconFailure(t *testing.T) {
	c := Client{log: testlogger.New(t)}
	clk := clock.NewFakeClock()
	validate := randomnessValidator(fakeChainInfo(), nil, &c, clk)

	msg := pubsub.Message{Message: &pb.Message{}}
	res := validate(context.Background(), randomPeerID(t), &msg)

	if res != pubsub.ValidationReject {
		t.Fatal(errors.New("expected reject for invalid message"))
	}
}

func TestAcceptsWithoutTrustRoot(t *testing.T) {
	c := Client{log: testlogger.New(t)}
	clk := clock.NewFakeClock()
	validate := randomnessValidator(nil, nil, &c, clk)

	resp := drand.PublicRandResponse{}
	data, err := proto.Marshal(&resp)
	if err != nil {
		t.Fatal(err)
	}
	msg := pubsub.Message{Message: &pb.Message{Data: data}}
	res := validate(context.Background(), randomPeerID(t), &msg)

	if res != pubsub.ValidationAccept {
		t.Fatal(errors.New("expected accept without trust root"))
	}
}

func TestRejectsFutureBeacons(t *testing.T) {
	info := fakeChainInfo()
	c := Client{log: testlogger.New(t)}
	clk := clock.NewFakeClock()
	validate := randomnessValidator(info, nil, &c, clk)

	resp := drand.PublicRandResponse{
		Round: common.CurrentRound(time.Now().Unix(), info.Period, info.GenesisTime) + 5,
	}
	data, err := proto.Marshal(&resp)
	if err != nil {
		t.Fatal(err)
	}
	msg := pubsub.Message{Message: &pb.Message{Data: data}}
	res := validate(context.Background(), randomPeerID(t), &msg)

	if res != pubsub.ValidationReject {
		t.Fatal(errors.New("expected reject for future message"))
	}
}

func TestRejectsVerifyBeaconFailure(t *testing.T) {
	info := fakeChainInfo()
	c := Client{log: testlogger.New(t)}
	clk := clock.NewFakeClock()
	validate := randomnessValidator(info, nil, &c, clk)

	resp := drand.PublicRandResponse{
		Round: common.CurrentRound(time.Now().Unix(), info.Period, info.GenesisTime),
		// missing signature etc.
	}
	data, err := proto.Marshal(&resp)
	if err != nil {
		t.Fatal(err)
	}
	msg := pubsub.Message{Message: &pb.Message{Data: data}}
	res := validate(context.Background(), randomPeerID(t), &msg)

	if res != pubsub.ValidationReject {
		t.Fatal(errors.New("expected reject for beacon verification failure"))
	}
}

func TestIgnoresCachedEqualBeacon(t *testing.T) {
	info := fakeChainInfo()
	ca := cache.NewMapCache()
	c := Client{log: testlogger.New(t)}
	clk := clock.NewFakeClockAt(time.Now())
	validate := randomnessValidator(info, ca, &c, clk)
	rdata := fakeRandomData(info, clk)

	ca.Add(rdata.Rnd, &rdata)

	resp := drand.PublicRandResponse{
		Round:             rdata.Rnd,
		Signature:         rdata.Sig,
		PreviousSignature: rdata.PreviousSignature,
		Randomness:        rdata.Random,
	}
	data, err := proto.Marshal(&resp)
	if err != nil {
		t.Fatal(err)
	}
	msg := pubsub.Message{Message: &pb.Message{Data: data}}
	res := validate(context.Background(), randomPeerID(t), &msg)

	if res != pubsub.ValidationIgnore {
		t.Fatal(errors.New("expected ignore for cached beacon"))
	}
}

func TestRejectsCachedUnequalBeacon(t *testing.T) {
	info := fakeChainInfo()
	ca := cache.NewMapCache()
	c := Client{log: testlogger.New(t)}
	clk := clock.NewFakeClock()
	validate := randomnessValidator(info, ca, &c, clk)
	rdata := fakeRandomData(info, clk)

	ca.Add(rdata.Rnd, &rdata)

	sig := make([]byte, 8)
	binary.LittleEndian.PutUint64(sig, rdata.Rnd+1)

	resp := drand.PublicRandResponse{
		Round:             rdata.Rnd,
		Signature:         rdata.Sig,
		PreviousSignature: sig, // incoming message has incorrect previous sig
		Randomness:        rdata.Random,
	}
	data, err := proto.Marshal(&resp)
	if err != nil {
		t.Fatal(err)
	}
	msg := pubsub.Message{Message: &pb.Message{Data: data}}
	res := validate(context.Background(), randomPeerID(t), &msg)

	if res != pubsub.ValidationReject {
		t.Fatal(fmt.Errorf("expected reject for cached but unequal beacon, got: %v", res))
	}
}

func TestIgnoresCachedEqualNonRandomDataBeacon(t *testing.T) {
	info := fakeChainInfo()
	ca := cache.NewMapCache()
	c := Client{log: testlogger.New(t)}
	clk := clock.NewFakeClockAt(time.Now())
	validate := randomnessValidator(info, ca, &c, clk)
	rdata := randomDataWrapper{fakeRandomData(info, clk)}

	ca.Add(rdata.GetRound(), &rdata)

	resp := drand.PublicRandResponse{
		Round:             rdata.GetRound(),
		Signature:         rdata.GetSignature(),
		PreviousSignature: rdata.data.PreviousSignature,
		Randomness:        rdata.GetRandomness(),
	}
	data, err := proto.Marshal(&resp)
	if err != nil {
		t.Fatal(err)
	}
	msg := pubsub.Message{Message: &pb.Message{Data: data}}
	res := validate(context.Background(), randomPeerID(t), &msg)

	if res != pubsub.ValidationIgnore {
		t.Fatal(errors.New("expected ignore for cached beacon"))
	}
}

func TestRejectsCachedEqualNonRandomDataBeacon(t *testing.T) {
	info := fakeChainInfo()
	ca := cache.NewMapCache()
	c := Client{log: testlogger.New(t)}
	clk := clock.NewFakeClock()
	validate := randomnessValidator(info, ca, &c, clk)
	rdata := randomDataWrapper{fakeRandomData(info, clk)}

	ca.Add(rdata.GetRound(), &rdata)

	sig := make([]byte, 8)
	binary.LittleEndian.PutUint64(sig, rdata.GetRound()+1)

	resp := drand.PublicRandResponse{
		Round:             rdata.GetRound(),
		Signature:         sig, // incoming message has incorrect sig
		PreviousSignature: rdata.data.PreviousSignature,
		Randomness:        rdata.GetRandomness(),
	}
	data, err := proto.Marshal(&resp)
	if err != nil {
		t.Fatal(err)
	}
	msg := pubsub.Message{Message: &pb.Message{Data: data}}
	res := validate(context.Background(), randomPeerID(t), &msg)

	if res != pubsub.ValidationReject {
		t.Fatal(errors.New("expected reject for cached beacon"))
	}
}
