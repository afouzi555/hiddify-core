package hcore

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/hiddify/hiddify-core/v2/config"
	"github.com/hiddify/hiddify-core/v2/db"
	hcommon "github.com/hiddify/hiddify-core/v2/hcommon"
	service_manager "github.com/hiddify/hiddify-core/v2/service_manager"
	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/experimental/libbox"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/service"
)

func (s *CoreService) Start(ctx context.Context, in *StartRequest) (*CoreInfoResponse, error) {
	return Start(static.BaseContext, in)
}

func Start(ctx context.Context, in *StartRequest) (*CoreInfoResponse, error) {
	return StartService(ctx, in)
}

func (s *CoreService) StartService(ctx context.Context, in *StartRequest) (*CoreInfoResponse, error) {
	return StartService(ctx, in)
}

func saveLastStartRequest(in *StartRequest) error {
	if in.ConfigContent == "" && in.ConfigPath == "" {
		return nil
	}
	settings := db.GetTable[hcommon.AppSettings]()
	return settings.UpdateInsert(
		&hcommon.AppSettings{
			Id:    "lastStartRequestPath",
			Value: in.ConfigPath,
		},
		&hcommon.AppSettings{
			Id:    "lastStartRequestContent",
			Value: in.ConfigContent,
		},
		&hcommon.AppSettings{
			Id:    "lastStartRequestName",
			Value: in.ConfigName,
		},
	)
}

func loadLastStartRequestIfNeeded(in *StartRequest) (*StartRequest, error) {
	if in != nil && (in.ConfigContent != "" || in.ConfigPath != "") {
		return in, nil
	}
	settings := db.GetTable[hcommon.AppSettings]()
	lastPath, err := settings.Get("lastStartRequestPath")
	if err != nil {
		return nil, err
	}
	lastContent, err := settings.Get("lastStartRequestContent")
	if err != nil {
		return nil, err
	}

	lastName, err := settings.Get("lastStartRequestName")
	if err != nil {
		return nil, err
	}
	return &StartRequest{
		ConfigPath:    lastPath.Value.(string),
		ConfigContent: lastContent.Value.(string),
		ConfigName:    lastName.Value.(string),
	}, nil
}

func StartService(ctx context.Context, in *StartRequest) (coreResponse *CoreInfoResponse, err error) {
	defer config.DeferPanicToError("startmobile", func(recovered_err error) {
		coreResponse, err = errorWrapper(MessageType_UNEXPECTED_ERROR, recovered_err)
	})
	hclog(10, "StartService() entered", fmt.Sprintf("globalPlatformInterface_is_nil=%v coreState=%v", static.globalPlatformInterface == nil, static.CoreState))
	static.lock.Lock()
	defer static.lock.Unlock()

	if static.CoreState != CoreStates_STOPPED {
		// return errorWrapper(MessageType_ALREADY_STARTED, fmt.Errorf("instance already started"))
		return &CoreInfoResponse{
			CoreState:   static.CoreState,
			MessageType: MessageType_ALREADY_STARTED,
			Message:     "instance already started",
		}, nil
	}
	SetCoreStatus(CoreStates_STARTING, MessageType_EMPTY, "")

	in, err = loadLastStartRequestIfNeeded(in)
	if err != nil {
		return errorWrapper(MessageType_ERROR_BUILDING_CONFIG, err)
	}

	static.previousStartRequest = in
	options, err := BuildConfig(ctx, in)
	if err != nil {
		hclog(11, "BuildConfig FAILED", err.Error())
		return errorWrapper(MessageType_ERROR_BUILDING_CONFIG, err)
	}
	hclog(11, "BuildConfig OK, options built")
	saveLastStartRequest(in)

	Log(LogLevel_DEBUG, LogType_CORE, "Main Service pre start")
	if err := service_manager.OnMainServicePreStart(options); err != nil {
		return errorWrapper(MessageType_ERROR_EXTENSION, err)
	}
	currentBuildConfigPath := filepath.Join(sWorkingPath, "data/current-config.json")
	Log(LogLevel_DEBUG, LogType_CORE, "Saving config to ", currentBuildConfigPath)

	config.SaveCurrentConfig(ctx, currentBuildConfigPath, *options)
	if static.debug {
		pout, err := options.MarshalJSONContext(ctx)
		if err != nil {
			return errorWrapper(MessageType_ERROR_BUILDING_CONFIG, err)
		}
		Log(LogLevel_INFO, LogType_CORE, "Current Config is:\n", string(pout))
	}
	hclog(12, "about to wrap+register platformInterface for this session", fmt.Sprintf("globalPlatformInterface_is_nil=%v", static.globalPlatformInterface == nil))
	ctx = libbox.FromContext(ctx, static.globalPlatformInterface)
	if static.globalPlatformInterface != nil {
		platformWrapper := libbox.WrapPlatformInterface(static.globalPlatformInterface)
		hclog(13, "platformWrapper built OK (UseProcFS() succeeded -- underlying Java ref was alive at this point)")
		service.MustRegister[adapter.PlatformInterface](ctx, platformWrapper)
		hclog(14, "platformWrapper registered into service registry")
		// } else {
		// 	service.MustRegister[adapter.PlatformInterface](ctx, (*adapter.PlatformInterface)nil)
	} else {
		hclog(13, "SKIPPED wrapping -- globalPlatformInterface is nil, native netlink fallback will be used")
	}
	Log(LogLevel_DEBUG, LogType_CORE, "Stating Service with delay ?", in.DelayStart)
	if in.DelayStart {
		<-time.After(1000 * time.Millisecond)
	}
	hclog(18, "about to call libbox.SetMemoryLimit()", fmt.Sprintf("in.DisableMemoryLimit=%v effective_enabled=%v", in.DisableMemoryLimit, C.IsIos || !in.DisableMemoryLimit))
	libbox.SetMemoryLimit(C.IsIos || !in.DisableMemoryLimit)
	hclog(15, "about to call NewService() -- if step 16 never logs, the native crash is inside this call")
	instance, err := NewService(ctx, *options)
	if err != nil {
		hclog(16, "NewService FAILED", err.Error())
		return errorWrapper(MessageType_START_SERVICE, err)
	}
	hclog(16, "NewService() returned OK")
	static.StartedService = instance
	// D155 -- REAL ROOT CAUSE, found by reading gomobile's own reference-counting source
	// directly (github.com/sagernet/gomobile bind/java/Seq.java + bind/java/seq_android.c.support)
	// after 5 prior targeted fixes (DefaultNetworkMonitor break, mobile.go context reuse,
	// Setup() clobber guard, aggressive-memory-limit default) each genuinely fixed a real bug
	// but none stopped the Nord 5G crash -- the exact "Unknown reference: N" abort kept
	// recurring with the SAME refnum (42 -- Seq.java's REF_OFFSET, i.e. always the very
	// first Java object registered in a session: the platform interface itself) at a
	// DIFFERENT PlatformInterface method each time (GetInterfaces, then OpenTun, then
	// AutoDetectInterfaceControl), regardless of the fix applied.
	//
	// The mechanism: every Go->Java call through a refnum does a temporary, balanced
	// increment+decrement around that one call (seq_android.c.support's go_seq_from_refnum,
	// "Go incremented the reference count just before passing the refnum. Decrement it
	// here."). The BASELINE hold from the original Mobile.setup() registration is separate,
	// and depends on something on the Go side keeping the wrapped platformInterface
	// (platformWrapper, built via libbox.WrapPlatformInterface() above and registered into
	// ctx's service.Registry) REACHABLE for Go's own garbage collector. ctx here was a
	// local variable, going out of scope the moment StartService() returned; nothing else
	// in this function chain kept the registry (and therefore platformWrapper) pinned for
	// the actual, asynchronous lifetime of the running VPN tunnel. Whenever Go's GC next
	// ran a cycle -- unrelated to memory limits, purely whenever it happened to run -- it
	// found platformWrapper unreachable, collected it, and its finalizer released the
	// baseline JNI reference for refnum 42. The next PlatformInterface call afterward --
	// whichever one happened to be next -- aborted with "Unknown reference: 42".
	//
	// Fix: retain ctx itself for as long as the service runs (cleared in Stop(), stop.go),
	// so the registry and everything registered in it stay reachable the whole time.
	static.RunningServiceContext = ctx
	if static.debug {
		dumpGoroutinesToFile(fmt.Sprint(sWorkingPath, "/data/goroutine-start.log"))
	}
	for inb := range options.Inbounds {
		if opts, ok := options.Inbounds[inb].Options.(option.SocksInboundOptions); ok {
			static.ListenPort = opts.ListenPort
		}
	}

	hclog(17, "StartService() completed normally, returning STARTED")
	return SetCoreStatus(CoreStates_STARTED, MessageType_EMPTY, ""), nil
}
