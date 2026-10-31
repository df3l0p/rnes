package harness

import (
	"strings"
	"testing"
)

func TestSandboxPrepare(t *testing.T) {
	tests := []struct {
		name      string
		opts      Options
		exit      map[string]int
		wantCalls []string // argv prefixes, in order
		wantImage string   // prefix of the image sessions will run
		wantErr   string
	}{
		{
			name: "base and image present locally: nothing to do",
			opts: Options{Sandbox: true},
			wantCalls: []string{
				"image inspect rnes-sandbox-base-opencode:",
				"image inspect rnes-sandbox-default:",
				"image inspect rnes-sandbox-proxy:",
			},
			wantImage: "rnes-sandbox-default:",
		},
		{
			name: "image missing: built on the base with RNES_BASE, never pulled",
			opts: Options{Sandbox: true},
			exit: map[string]int{"image inspect rnes-sandbox-default:": 1},
			wantCalls: []string{
				"image inspect rnes-sandbox-base-opencode:",
				"image inspect rnes-sandbox-default:",
				"build --build-arg RNES_BASE=rnes-sandbox-base-opencode:",
				"image inspect rnes-sandbox-proxy:",
			},
			wantImage: "rnes-sandbox-default:",
		},
		{
			name: "both missing: base built first, then the image",
			opts: Options{Sandbox: true},
			exit: map[string]int{"image inspect": 1},
			wantCalls: []string{
				"image inspect rnes-sandbox-base-opencode:",
				"build -t rnes-sandbox-base-opencode:",
				"image inspect rnes-sandbox-default:",
				"build --build-arg RNES_BASE=rnes-sandbox-base-opencode:",
				"image inspect rnes-sandbox-proxy:",
				"build -t rnes-sandbox-proxy:",
			},
			wantImage: "rnes-sandbox-default:",
		},
		{
			name: "-build rebuilds base and image without inspecting or using the layer cache",
			opts: Options{Sandbox: true, Build: true},
			wantCalls: []string{
				"build --no-cache -t rnes-sandbox-base-opencode:",
				"build --no-cache --build-arg RNES_BASE=rnes-sandbox-base-opencode:",
				"build --no-cache -t rnes-sandbox-proxy:",
			},
			wantImage: "rnes-sandbox-default:",
		},
		{
			name:      "base build failure is an error and stops there",
			opts:      Options{Sandbox: true},
			exit:      map[string]int{"image inspect": 1, "build": 1},
			wantCalls: []string{"image inspect", "build"},
			wantErr:   "building rnes-sandbox-base-opencode:",
		},
		{
			name: "image build failure is an error",
			opts: Options{Sandbox: true},
			exit: map[string]int{"image inspect rnes-sandbox-default:": 1, "build": 1},
			wantCalls: []string{
				"image inspect rnes-sandbox-base-opencode:",
				"image inspect rnes-sandbox-default:",
				"build",
			},
			wantErr: "building rnes-sandbox-default:",
		},
		{
			name:    "unknown image fails before docker is touched",
			opts:    Options{Sandbox: true, Image: "nope"},
			wantErr: "available: default",
		},
		{
			name:    "internal base images are not selectable",
			opts:    Options{Sandbox: true, Image: "base-opencode"},
			wantErr: "unknown sandbox image",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logPath := fakeDocker(t, tt.exit)
			s := newSandbox(tt.opts, "")
			err := s.Prepare()
			if tt.wantErr == "" && err != nil {
				t.Fatalf("Prepare: %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("Prepare error = %v, want it to contain %q", err, tt.wantErr)
			}
			got := calls(t, logPath)
			if len(got) != len(tt.wantCalls) {
				t.Fatalf("docker calls = %q, want prefixes %q", got, tt.wantCalls)
			}
			for i, c := range got {
				if !strings.HasPrefix(c, tt.wantCalls[i]) {
					t.Errorf("call %d = %q, want prefix %q", i, c, tt.wantCalls[i])
				}
			}
			if tt.wantImage != "" && !strings.HasPrefix(s.image, tt.wantImage) {
				t.Errorf("run image = %q, want prefix %q", s.image, tt.wantImage)
			}
			if tt.wantErr == "" && !strings.HasPrefix(s.proxyImage, "rnes-sandbox-proxy:") {
				t.Errorf("proxyImage = %q, want prefix %q", s.proxyImage, "rnes-sandbox-proxy:")
			}
		})
	}
}

func TestPrepareAllHarnessesBuildsEveryHarnessBaseAndProxyOnce(t *testing.T) {
	logPath := fakeDocker(t, nil)
	if err := PrepareAllHarnesses(Options{Build: true}); err != nil {
		t.Fatalf("PrepareAllHarnesses: %v", err)
	}
	got := calls(t, logPath)
	wantPrefixes := []string{
		"info",
		"build --no-cache -t rnes-sandbox-base-agy:",
		"build --no-cache --build-arg RNES_BASE=rnes-sandbox-base-agy:",
		"build --no-cache -t rnes-sandbox-base-opencode:",
		"build --no-cache --build-arg RNES_BASE=rnes-sandbox-base-opencode:",
		"build --no-cache -t rnes-sandbox-proxy:",
	}
	if len(got) != len(wantPrefixes) {
		t.Fatalf("docker calls = %q, want prefixes %q", got, wantPrefixes)
	}
	for i, c := range got {
		if !strings.HasPrefix(c, wantPrefixes[i]) {
			t.Errorf("call %d = %q, want prefix %q", i, c, wantPrefixes[i])
		}
	}
}

func TestImageTag(t *testing.T) {
	a := imageTag("default", []byte("FROM x"), "rnes-sandbox-base-opencode:1")
	if a != imageTag("default", []byte("FROM x"), "rnes-sandbox-base-opencode:1") {
		t.Error("tag must be stable for identical inputs")
	}
	if a == imageTag("default", []byte("FROM y"), "rnes-sandbox-base-opencode:1") {
		t.Error("tag must change with the Dockerfile content")
	}
	if a == imageTag("default", []byte("FROM x"), "rnes-sandbox-base-agy:1") {
		t.Error("a layer's tag must change when its base's tag does")
	}
	if !strings.HasPrefix(a, "rnes-sandbox-default:") {
		t.Errorf("tag %q must be rnes-sandbox-<name>:<hash>", a)
	}
}

func TestLayerBuildArgs(t *testing.T) {
	got := strings.Join(layerBuildArgs("rnes-sandbox-default:h", "rnes-sandbox-base-opencode:b"), " ")
	want := "build --build-arg RNES_BASE=rnes-sandbox-base-opencode:b -t rnes-sandbox-default:h -"
	if got != want {
		t.Errorf("args = %q, want %q", got, want)
	}
}
