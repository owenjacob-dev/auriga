package stellarcfg

import (
	"reflect"
	"strings"
	"testing"
)

func TestArchiveBackends(t *testing.T) {
	nginx := ArchiveSpec{Name: "acme-a", Backend: BackendNginx, URL: "https://history-a.acme.org/", Dir: "/var/www/history/"}
	h, err := nginx.History(true)
	if err != nil {
		t.Fatal(err)
	}
	if h != (HistoryArchive{Name: "acme-a", Get: "curl -sf https://history-a.acme.org/{0} -o {1}", Put: "cp {0} /var/www/history/{1}", Mkdir: "mkdir -p /var/www/history/{0}"}) {
		t.Errorf("nginx %+v", h)
	}
	if !reflect.DeepEqual(nginx.Binaries(true), []string{"curl", "cp", "mkdir"}) || !reflect.DeepEqual(nginx.Binaries(false), []string{"curl"}) {
		t.Errorf("binaries %v %v", nginx.Binaries(true), nginx.Binaries(false))
	}
	if nginx.WellKnownURL() != "https://history-a.acme.org/.well-known/stellar-history.json" {
		t.Error(nginx.WellKnownURL())
	}

	r2 := ArchiveSpec{Name: "acme-b", Backend: BackendS3, URL: "https://history-b.acme.org", Bucket: "acme-history", Prefix: "b", Endpoint: "https://abc.r2.cloudflarestorage.com"}
	h, _ = r2.History(true)
	if h.Put != "aws s3 cp {0} s3://acme-history/b/{1} --endpoint-url https://abc.r2.cloudflarestorage.com" || h.Mkdir != "" {
		t.Errorf("r2 %+v", h)
	}
	s3 := r2
	s3.Endpoint, s3.Prefix = "", ""
	h, _ = s3.History(true)
	if h.Put != "aws s3 cp {0} s3://acme-history/{1}" {
		t.Errorf("s3 %+v", h)
	}
	if r2.PutTarget() == s3.PutTarget() {
		t.Error("different prefixes share a put target")
	}

	read := ArchiveSpec{Name: "x", Backend: BackendNginx, URL: "https://h.example", Dir: "/srv"}
	if h, _ := read.History(false); h.Put != "" || h.Mkdir != "" {
		t.Errorf("read-only node publishes: %+v", h)
	}

	custom := ArchiveSpec{Name: "c", Backend: BackendCustom, Get: "/usr/local/bin/fetch {0} {1}", Put: "rclone copyto {0} r:arch/{1}"}
	h, _ = custom.History(true)
	if h.Get != custom.Get || h.Put != custom.Put || !reflect.DeepEqual(custom.Binaries(true), []string{"fetch", "rclone"}) {
		t.Errorf("custom %+v %v", h, custom.Binaries(true))
	}
}

func TestArchiveRejectsShellInjection(t *testing.T) {
	bad := []ArchiveSpec{
		{Name: "a b", Backend: BackendNginx, URL: "https://h", Dir: "/srv"},
		{Name: "a", Backend: BackendNginx, URL: "https://h/$(reboot)", Dir: "/srv"},
		{Name: "a", Backend: BackendNginx, URL: "https://h", Dir: "/srv; rm -rf /"},
		{Name: "a", Backend: BackendNginx, URL: "https://h", Dir: "/srv/../etc"},
		{Name: "a", Backend: BackendNginx, URL: "ftp://h", Dir: "/srv"},
		{Name: "a", Backend: BackendS3, URL: "https://h", Bucket: "Bad_Bucket"},
		{Name: "a", Backend: BackendS3, URL: "https://h", Bucket: "ok-bucket", Prefix: "p`id`"},
		{Name: "a", Backend: BackendS3, URL: "https://h", Bucket: "ok-bucket", Endpoint: "https://e && id"},
		{Name: "a", Backend: BackendCustom},
		{Name: "a", Backend: BackendCustom, Get: "cp {0} {1}\nrm -rf /"},
		{Name: "a", Backend: "ftp"},
	}
	for _, a := range bad {
		if err := a.Validate(); err == nil {
			t.Errorf("accepted %+v", a)
		}
		if _, err := a.History(true); err == nil {
			t.Errorf("History accepted %+v", a)
		}
	}
	if !strings.Contains((ArchiveSpec{Name: "a", Backend: BackendNginx, URL: "x", Dir: "/srv"}).Validate().Error(), "plain http(s) URL") {
		t.Error("message")
	}
}

func TestArchiveFromHistory(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec ArchiveSpec
	}{
		{"nginx", ArchiveSpec{Name: "acme-a", Backend: BackendNginx, URL: "https://history-a.acme.org", Dir: "/var/www/history"}},
		{"s3", ArchiveSpec{Name: "acme-b", Backend: BackendS3, URL: "https://history-b.acme.org", Bucket: "acme-history", Prefix: "b"}},
		{"r2", ArchiveSpec{Name: "acme-c", Backend: BackendS3, URL: "https://h.acme.org/c", Bucket: "acme-history", Prefix: "nodes/c", Endpoint: "https://x.r2.cloudflarestorage.com"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, err := tc.spec.History(true)
			if err != nil {
				t.Fatal(err)
			}
			if got := ArchiveFromHistory(h); got != tc.spec || !got.Same(h) {
				t.Errorf("got %+v, want %+v", got, tc.spec)
			}
		})
	}
	// Hand-written tables stay as they are, as custom commands.
	local := HistoryArchive{Name: "local", Get: "cp /var/lib/stellar/history/{0} {1}", Put: "cp {0} /var/lib/stellar/history/{1}", Mkdir: "mkdir -p /var/lib/stellar/history/{0}"}
	got := ArchiveFromHistory(local)
	if got.Backend != BackendCustom || got.URL != "" || got.Get != local.Get || got.Put != local.Put || got.Mkdir != local.Mkdir || !got.Same(local) {
		t.Errorf("local: %+v", got)
	}
	// A curl get with a put the guided backends would write differently.
	odd := HistoryArchive{Name: "odd", Get: "curl -sf https://h.example/{0} -o {1}", Put: "cp {0} /srv/h/{1} && sync", Mkdir: "mkdir -p /srv/h/{0}"}
	if got := ArchiveFromHistory(odd); got.Backend != BackendCustom || got.URL != "https://h.example" || !got.Same(odd) {
		t.Errorf("odd: %+v", got)
	}
	if (ArchiveSpec{Name: "local", Backend: BackendNginx, URL: "https://h.example", Dir: "/elsewhere"}).Same(local) {
		t.Error("a different put counted as the same archive")
	}
}
