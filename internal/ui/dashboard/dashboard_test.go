package dashboard

import (
	"testing"

	"github.com/owenjacob-dev/auriga/internal/corehttp"
	"github.com/owenjacob-dev/auriga/internal/health"
	"github.com/owenjacob-dev/auriga/internal/sshx"
	"github.com/owenjacob-dev/auriga/internal/ui"
	"github.com/owenjacob-dev/auriga/internal/ui/theme"
)

func TestLabel(t *testing.T) {
	synced := health.Row{State: health.Cell{Text: health.SyncedState}}
	catching := health.Row{State: health.Cell{Text: "Catching up"}}
	staleSynced := health.Row{State: health.Cell{Text: health.SyncedState, Stale: true}}
	connected := sshx.HostState{State: sshx.StateConnected}
	info := &corehttp.Info{}

	for _, tc := range []struct {
		name string
		h    ui.HostView
		r    health.Row
		st   theme.Status
		want string
	}{
		{"synced but too few peers", ui.HostView{Conn: connected, Info: info}, synced, theme.StatusDown, "synced"},
		{"synced and healthy", ui.HostView{Conn: connected, Info: info}, synced, theme.StatusOK, "synced"},
		{"not synced", ui.HostView{Conn: connected, Info: info}, catching, theme.StatusDegraded, "degraded"},
		{"synced but stale", ui.HostView{Conn: connected, Info: info}, staleSynced, theme.StatusDown, "down"},
		{"synced but last poll failed", ui.HostView{Conn: connected, Info: info, InfoErr: "timeout"}, synced, theme.StatusDegraded, "degraded"},
		{"disconnected", ui.HostView{Conn: sshx.HostState{State: sshx.StateDisconnected}, Info: info}, synced, theme.StatusDown, "down"},
		{"no info yet", ui.HostView{Conn: connected}, synced, theme.StatusDegraded, "degraded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Label(&tc.h, tc.r, tc.st); got != tc.want {
				t.Errorf("Label = %q, want %q", got, tc.want)
			}
		})
	}
}
