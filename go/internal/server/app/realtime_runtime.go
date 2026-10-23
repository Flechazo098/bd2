package app

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync/atomic"
	"time"

	"bd2server/internal/server/design/gameconfig"
	"bd2server/internal/server/design/gamedata"
	eventplay "bd2server/internal/server/domain/events/play"
	"bd2server/internal/server/gateway/realtime"
	"bd2server/internal/server/gateway/session"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/runtime/room"
)

type realtimeRuntime struct {
	rooms   *room.Manager
	server  *realtime.Server
	tickets *realtimeTickets
	results *roomResults
	cancel  context.CancelFunc
	done    chan error
}

func openRealtime(c *configuration, registry *playerRegistry, game *session.Server, shared *sharedRuntime, instance string) (*realtimeRuntime, error) {
	config := c.gameRules.Realtime
	if !config.Enabled {
		return nil, nil
	}
	results := shared.rooms
	results.endpoints = []eventplay.RelayEndpoint{{Region: "1", Host: config.PublicHost, TCPPort: strconv.Itoa(config.TCPPort)}}
	var transport atomic.Pointer[realtime.Server]
	rooms, err := room.New(room.Config{AdvertisedHost: config.PublicHost, TCPPort: config.TCPPort, UDPPort: config.UDPPort,
		SquareChannels: int(registry.factory.design.avatarProfile.SquareChannels), SquareChannelCapacity: int(registry.factory.design.avatarProfile.SquareChannelCapacity),
		Publish: func(frames []room.Delivery) {
			if server := transport.Load(); server != nil {
				server.Deliver(frames)
			}
		},
		PublishResult: results.publish,
	}, roomRules{players: registry, shared: shared})
	if err != nil {
		return nil, err
	}
	results.rooms = rooms
	tickets := &realtimeTickets{game: game, players: registry, rooms: rooms, bootID: instance,
		endpoint: realtimeEndpoint{TCPHost: config.PublicHost, TCPPort: config.TCPPort, UDPHost: config.PublicHost, UDPPort: config.UDPPort, TLSServerName: config.PublicHost, DevelopmentPlain: config.DevelopmentPlainLoopback},
		ownerIndex: func(ctx context.Context, account string) (int64, error) {
			if registry.factory.profiles != nil {
				profile, err := registry.factory.profiles.GameIdentity(ctx, account)
				return profile.OwnerIndex, err
			}
			owner, _, err := wire.Varint(registry.factory.seeds.login.UserInfo, 1)
			return int64(owner), err
		},
	}
	resolve := func(path string) string {
		if path == "" {
			return path
		}
		return gameconfig.ResolvePath(c.gameConfigPath, path)
	}
	chatDesign, err := gamedata.LoadChatDesign(c.gameData, c.gameDataVersion)
	if err != nil {
		return nil, errors.Join(err, rooms.Close(context.Background()))
	}
	chat := &realtimeChat{rooms: rooms, shared: shared, rules: roomRules{players: registry, shared: shared}, historyCount: int(chatDesign.GuildHistoryCount), historyAge: time.Duration(chatDesign.GuildHistoryHours) * time.Hour, emojis: chatDesign.Emojis, now: time.Now}
	server, err := realtime.New(realtime.Config{TCPListen: config.TCPListen, UDPListen: config.UDPListen, TLSCertFile: resolve(config.TLSCertFile), TLSKeyFile: resolve(config.TLSKeyFile), DevelopmentPlainLoopback: config.DevelopmentPlainLoopback}, chat, tickets)
	if err != nil {
		return nil, errors.Join(err, rooms.Close(context.Background()))
	}
	transport.Store(server)
	results.deliver = server.Deliver
	results.start()
	registry.factory.roomResults = results
	shared.rooms = results
	c.bootstrap.MatchingAddress = net.JoinHostPort(config.PublicHost, strconv.Itoa(config.TCPPort))
	return &realtimeRuntime{rooms: rooms, server: server, tickets: tickets, results: results}, nil
}

func (r *realtimeRuntime) start() {
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.done = make(chan error, 1)
	go func() { r.done <- r.server.Serve(ctx) }()
}

func (r *realtimeRuntime) close() error {
	if r == nil {
		return nil
	}
	if r.cancel != nil {
		r.cancel()
	}
	err := r.server.Close(context.Background())
	err = errors.Join(err, r.rooms.Close(context.Background()))
	r.results.stop()
	return err
}
