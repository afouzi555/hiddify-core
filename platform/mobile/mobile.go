package mobile

import (
	hcore "github.com/hiddify/hiddify-core/v2/hcore"

	_ "net/http/pprof"

	_ "github.com/sagernet/gomobile"
	"github.com/sagernet/sing-box/experimental/libbox"
)

type SetupOptions struct {
	BasePath        string
	WorkingDir      string
	TempDir         string
	Listen          string
	Secret          string
	Debug           bool
	Mode            int
	FixAndroidStack bool
}

func Setup(opt *SetupOptions, platformInterface libbox.PlatformInterface) error {
	return hcore.Setup(&hcore.SetupRequest{
		BasePath:          opt.BasePath,
		WorkingDir:        opt.WorkingDir,
		TempDir:           opt.TempDir,
		FlutterStatusPort: 0,
		Listen:            opt.Listen,
		Debug:             opt.Debug,
		Mode:              hcore.SetupMode(opt.Mode),
		Secret:            opt.Secret,
		FixAndroidStack:   opt.FixAndroidStack,
	}, platformInterface)

	// return hcore.Start(17078)
}

// func Start(configPath string, configContent string, platformInterface libbox.PlatformInterface) (*hcore.CoreInfoResponse, error) {
// 	state, err := hcore.StartWithPlatformInterface(&hcore.StartRequest{
// 		ConfigContent: configContent,
// 		ConfigPath:    configPath,
// 	}, platformInterface)
// 	return state, err
// }

// BUG FIX (2026-09-03): this used to build a FRESH context via
// libbox.BaseContext(nil) -- discarding the platform interface that Setup()
// already wired up and stored in hcore's own base context. A nil platform
// interface makes sing-box's NetworkManager fall back to its native
// Linux netlink-based interface monitor instead of delegating to Kotlin's
// PlatformInterfaceWrapper (see route/network.go's
// usePlatformDefaultInterfaceMonitor := nm.platformInterface != nil check).
// Android's SELinux policy for untrusted_app blocks raw netlink route
// sockets, so that fallback crashes the whole process with a native SIGABRT
// (avc: denied bind ... tclass=netlink_route_socket, then gomobile's
// go/Seq: Unknown reference: N) the moment a connection is started --
// reproduced consistently on a OnePlus Nord 5G, which generates enough
// network-change churn to hit this path reliably; not reproduced on a
// lower-churn test device, which is presumably why this went unnoticed.
// hcore.GetBaseContext() reuses the context Setup() already built with the
// real platform interface embedded, instead of discarding it here.
func Start(configPath string, configContent string) error {
	_, err := hcore.StartService(hcore.GetBaseContext(), &hcore.StartRequest{
		ConfigPath:    configPath,
		ConfigContent: configContent,
	})
	return err
}

func Stop() error {
	_, err := hcore.Stop()
	return err
}

func GetServerPublicKey() []byte {
	return hcore.GetGrpcServerPublicKey()
}

func AddGrpcClientPublicKey(clientPublicKey []byte) error {
	return hcore.AddGrpcClientPublicKey(clientPublicKey)
}

func Close(mode int) {
	hcore.Close(hcore.SetupMode(mode))
}

func Test() string {
	return "Hello from mobile"
}

func Pause() {
	hcore.Pause()
}

func Wake() {
	hcore.Wake()
}
