package hcore

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
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

// D157 -- real GC-timing evidence, requested after D155 (context-retention) was tested and
// failed. GODEBUG=gctrace=1 (D156, hiddify-app Application.kt) turned out to be a dead end:
// Libbox.redirectStderr() (hiddify-sing-box/experimental/libbox/log.go) only wires up Go
// 1.23+'s debug.SetCrashOutput -- a hook for a *fatal Go runtime crash dump*, not a general
// stderr redirect. GODEBUG=gctrace's periodic "gc N @Ts ..." lines are written straight to
// the raw OS-level fd 2 by the runtime's own print functions, completely bypassing
// SetCrashOutput -- confirmed by reading hiddify-sing-box's own source. stderr.log has
// therefore never captured a GC cycle in this entire investigation, on any prior build.
//
// This replaces that dead end with a diagnostic built on the ALREADY-PROVEN hclog()/app.log
// channel instead: poll runtime.ReadMemStats() at each existing [log-hcore-connect] step,
// plus several times in the async window just after StartService() returns -- exactly where
// D155's evidence showed the crash actually happens (~60-260ms after CONNECTED, from a path
// unrelated to the synchronous ctx retained by D155). If NumGC increments between two of
// these polls, or LastGC lands inside that window, that's direct proof a GC cycle coincided
// with the crash; if NumGC never moves at all, the GC-reachability theory itself needs to be
// reconsidered rather than just its anchor point.
func hclogGCStats(step int, label string) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	lastGCAgo := "never"
	if m.LastGC > 0 {
		lastGCAgo = time.Since(time.Unix(0, int64(m.LastGC))).String()
	}
	hclog(step, label, fmt.Sprintf("NumGC=%d NumForcedGC=%d LastGC_ago=%s HeapObjects=%d",
		m.NumGC, m.NumForcedGC, lastGCAgo, m.HeapObjects))
}

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
	hclogGCStats(119, "gc-stats before wrap+register")
	hclog(12, "about to wrap+register platformInterface for this session", fmt.Sprintf("globalPlatformInterface_is_nil=%v", static.globalPlatformInterface == nil))
	ctx = libbox.FromContext(ctx, static.globalPlatformInterface)
	if static.globalPlatformInterface != nil {
		platformWrapper := libbox.WrapPlatformInterface(static.globalPlatformInterface)
		hclog(13, "platformWrapper built OK (UseProcFS() succeeded -- underlying Java ref was alive at this point)")
		service.MustRegister[adapter.PlatformInterface](ctx, platformWrapper)
		hclog(14, "platformWrapper registered into service registry")
		// D158 -- REAL FIX, found from D157's own GC-stats evidence: the previous anchor
		// (D155, static.RunningServiceContext = ctx set only AFTER NewService() returns) was
		// too late. D157's device test showed the "Unknown reference: 42" abort firing on a
		// background thread (AutoDetectInterfaceControl's async monitor) ~490ms INTO
		// NewService()'s own execution -- 38ms *before* NewService() even returned -- with 5
		// GC cycles (3 of them forced) landing in that exact window (NumGC 13 -> 18). D155's
		// anchor didn't exist yet at that point, so it protected nothing. Move the anchor to
		// immediately after registration instead, so ctx (and therefore platformWrapper,
		// reachable through the registry it's registered into) is pinned for Go's GC from the
		// moment the platform interface is registered, covering NewService()'s own
		// construction as well as the running tunnel's lifetime -- not just the latter.
		static.RunningServiceContext = ctx
		hclogGCStats(141, "gc-stats after platformWrapper registered")
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
	hclogGCStats(151, "gc-stats before NewService()")
	hclog(15, "about to call NewService() -- if step 16 never logs, the native crash is inside this call")
	instance, err := NewService(ctx, *options)
	if err != nil {
		hclog(16, "NewService FAILED", err.Error())
		static.RunningServiceContext = nil // D158 -- don't leave a stale anchor from a failed start
		return errorWrapper(MessageType_START_SERVICE, err)
	}
	hclog(16, "NewService() returned OK")
	hclogGCStats(161, "gc-stats after NewService() returned")
	static.StartedService = instance
	// ============================================================================
	// STATUS UPDATE (read this before trusting the theory below): D155's anchor was
	// moved earlier by D158 (see the comment right after step 14, above) on the theory
	// explained in this block. D158 was DEVICE-TESTED and the crash still occurred --
	// this means the GC-reachability-gap theory below, while a real and correctly-
	// understood mechanism (confirmed via gomobile source + real GC-timing device
	// evidence, D157), is NOT the complete explanation for the Nord 5G crash. A separate
	// comparison test (installing NekoBox for Android, an independent sing-box/gomobile
	// client, on the same device with the same profile -- it connected fine, zero crash)
	// proved the real bug is something Hiddify's own sing-box/gomobile integration does
	// differently from a normal single-Setup()-call client, not a gomobile defect this
	// retention fix could ever have addressed. Full evidence trail:
	// OPTIMUS_VPN_SECURITY_PRIVACY_DOC.md sections 9.4.3 (D157 evidence), 9.4.4 (D158's
	// disproof), 9.4.5 (the NekoBox comparison + correction), section 10 (methodology
	// lessons from this whole arc). This code is left in place -- it's a real, correct
	// fix for the mechanism it addresses, doesn't hurt anything, and may matter again if
	// that mechanism ever independently resurfaces -- but do not assume it, by itself,
	// is why any future Nord-5G Connect attempt does or doesn't crash.
	// ============================================================================
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
	// D158 -- static.RunningServiceContext is now set right after registration (see the
	// D158 comment above, right after step 14) instead of here, so it also covers
	// NewService()'s own execution. Left assigned to ctx again here too (harmless, ctx is
	// the same value) purely so a reader scanning forward from static.StartedService still
	// sees the anchor being maintained at this point.
	static.RunningServiceContext = ctx

	// D157 -- poll GC stats several times through the exact async window (StartService()
	// returning through ~1s later) where D155's evidence showed the crash actually happens.
	// Each poll lands in app.log via the already-proven hclog() channel.
	go func() {
		prev := time.Duration(0)
		for i, d := range []time.Duration{
			20 * time.Millisecond, 60 * time.Millisecond, 120 * time.Millisecond,
			250 * time.Millisecond, 500 * time.Millisecond, 1000 * time.Millisecond,
		} {
			time.Sleep(d - prev)
			prev = d
			hclogGCStats(171+i, fmt.Sprintf("gc-stats poll +%v after StartService() returned (async-crash window)", d))
		}
	}()

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
