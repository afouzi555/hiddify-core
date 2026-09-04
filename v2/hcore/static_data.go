package hcore

import (
	"context"
	"sync"
	"time"

	"github.com/hiddify/hiddify-core/v2/config"
	"github.com/sagernet/sing-box/common/monitoring"
	"github.com/sagernet/sing-box/daemon"
	"github.com/sagernet/sing-box/experimental/libbox"
	"github.com/sagernet/sing-box/log"
)

type HiddifyInstance struct {
	StartedService *daemon.StartedService
	// D155 -- see the comment on this field's assignment in start.go for the full root-cause
	// chain. Retains the exact context.Context passed to NewService() for as long as the
	// service is running, so the sing-box service.Registry (and therefore the wrapped
	// libbox.PlatformInterface registered into it) stays reachable to Go's own garbage
	// collector -- closing a real reachability gap that let the platform interface's
	// baseline gomobile/JNI reference get released mid-session.
	RunningServiceContext context.Context
	HiddifyOptions        *config.HiddifyOptions
	// activeConfigPath string
	CoreLogFactory            log.Factory
	coreInfoObserver          *monitoring.Broadcaster[*CoreInfoResponse]
	CoreState                 CoreStates
	logObserver               *monitoring.Broadcaster[*LogMessage]
	systemInfoObserver        *monitoring.Broadcaster[*SystemInfo]
	outboundsInfoObserver     *monitoring.Broadcaster[*OutboundGroupList]
	mainOutboundsInfoObserver *monitoring.Broadcaster[*OutboundGroupList]
	lock                      sync.Mutex
	globalPlatformInterface   libbox.PlatformInterface
	previousStartRequest      *StartRequest
	debug                     bool
	ListenPort                uint16
	BaseContext               context.Context
	endPauseTimer             *time.Timer // only for ios

	logLevel LogLevel
}

var static = &HiddifyInstance{
	CoreState:                 CoreStates_STOPPED,
	coreInfoObserver:          monitoring.NewBroadcaster[*CoreInfoResponse](context.Background()),
	logObserver:               monitoring.NewBroadcaster[*LogMessage](context.Background()),
	systemInfoObserver:        monitoring.NewBroadcaster[*SystemInfo](context.Background()),
	outboundsInfoObserver:     monitoring.NewBroadcaster[*OutboundGroupList](context.Background()),
	mainOutboundsInfoObserver: monitoring.NewBroadcaster[*OutboundGroupList](context.Background()),
}

// GetBaseContext returns the process-wide base context configured by Setup(),
// which has the platform interface (libbox.BaseContext(platformInterface))
// already embedded. Any code that starts a sing-box service after Setup() has
// run MUST use this context rather than building a fresh libbox.BaseContext(nil)
// -- a nil platform interface causes sing-box's NetworkManager to fall back to
// its native Linux netlink-based interface monitor (nm.platformInterface !=
// nil check in route/network.go), which Android's SELinux policy for
// untrusted_app blocks (avc: denied { bind } ... tclass=netlink_route_socket),
// crashing the whole process with a native SIGABRT the moment a connection is
// started. See platform/mobile/mobile.go's Start() for the caller-side half of
// this fix (2026-09-03).
func GetBaseContext() context.Context {
	return static.BaseContext
}
