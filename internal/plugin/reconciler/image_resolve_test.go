package reconciler

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
)

const (
	imgRepo           = "ghcr.io/example/stub"
	imgConfigDigest   = "sha256:cccc"
	imgManifestDigest = "sha256:dddd"
)

// Image shapes measured on real daemons: the classic store names a loaded
// image by its config digest, the containerd store by its manifest digest.
var (
	classicImage    = container.ImageInfo{ID: imgConfigDigest}
	containerdImage = container.ImageInfo{ID: imgManifestDigest}
)

type fakeImageRecords struct {
	rows map[string]db.PluginContainerImage
	err  error
}

func (f *fakeImageRecords) GetContainerImage(_ context.Context, digest string) (db.PluginContainerImage, error) {
	if f.err != nil {
		return db.PluginContainerImage{}, f.err
	}
	row, ok := f.rows[digest]
	if !ok {
		return db.PluginContainerImage{}, sql.ErrNoRows
	}
	return row, nil
}

func recordWithLocalID(id string) *fakeImageRecords {
	rec := db.PluginContainerImage{Digest: imgConfigDigest, Reference: imgRepo + "@" + imgConfigDigest}
	if id != "" {
		rec.LocalImageID = &id
	}
	return &fakeImageRecords{rows: map[string]db.PluginContainerImage{imgConfigDigest: rec}}
}

func imageRow(digest string) db.PluginContainer {
	return db.PluginContainer{
		ID:               "c-i1",
		PluginInstanceID: "i1",
		ImageRef:         imgRepo + "@" + digest,
		ImageDigest:      digest,
		ConfigHash:       "cfg-1",
		NetworkName:      "net-i1",
		DesiredState:     DesiredRunning,
	}
}

func TestResolveImage(t *testing.T) {
	tests := []struct {
		name    string
		image   *container.ImageInfo // what the runtime holds; nil = nothing
		records ImageRecords         // nil = Config.Images unset
		digest  string

		want         string
		wantMissing  bool   // errors.Is ErrImageUnavailable
		wantInErr    string // substring of the error
		wantOtherErr bool   // an error that is not ErrImageUnavailable
	}{
		{
			name:  "containerd store: recorded manifest-digest ID",
			image: &containerdImage, records: recordWithLocalID(imgManifestDigest),
			digest: imgConfigDigest, want: imgManifestDigest,
		},
		{
			name:  "classic store: recorded config-digest ID",
			image: &classicImage, records: recordWithLocalID(imgConfigDigest),
			digest: imgConfigDigest, want: imgConfigDigest,
		},
		{
			name:  "recorded image was pruned",
			image: nil, records: recordWithLocalID(imgManifestDigest),
			digest: imgConfigDigest, wantMissing: true, wantInErr: "pruned",
		},
		{
			name:  "recorded ID is not replaced by a bare-digest match",
			image: &classicImage, records: recordWithLocalID(imgManifestDigest),
			digest: imgConfigDigest, wantMissing: true,
		},
		{
			name:  "legacy row (NULL ID) on the classic store resolves by bare digest",
			image: &classicImage, records: recordWithLocalID(""),
			digest: imgConfigDigest, want: imgConfigDigest,
		},
		{
			name:  "legacy row (NULL ID) on the containerd store fails closed",
			image: &containerdImage, records: recordWithLocalID(""),
			digest: imgConfigDigest, wantMissing: true, wantInErr: "reinstall",
		},
		{
			name:  "no image record at all behaves like a legacy row",
			image: &classicImage, records: &fakeImageRecords{},
			digest: imgConfigDigest, want: imgConfigDigest,
		},
		{
			name:  "no records configured on the containerd store fails closed",
			image: &containerdImage, records: nil,
			digest: imgConfigDigest, wantMissing: true,
		},
		{
			name:  "row without a digest fails closed",
			image: &classicImage, records: nil,
			digest: "", wantMissing: true,
		},
		{
			name:  "record lookup failure is not reported as a missing image",
			image: &classicImage, records: &fakeImageRecords{err: errors.New("db down")},
			digest: imgConfigDigest, wantOtherErr: true, wantInErr: "db down",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := container.NewFake()
			fake.StrictImages = true
			if tt.image != nil {
				fake.AddImage(*tt.image)
			}
			r, err := New(Config{Runtime: fake, Store: &fakeStore{}, Images: tt.records})
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			got, err := r.resolveImage(context.Background(), imageRow(tt.digest))
			switch {
			case tt.wantMissing:
				if !errors.Is(err, ErrImageUnavailable) {
					t.Fatalf("resolveImage error = %v, want ErrImageUnavailable", err)
				}
			case tt.wantOtherErr:
				if err == nil || errors.Is(err, ErrImageUnavailable) {
					t.Fatalf("resolveImage error = %v, want a non-ErrImageUnavailable error", err)
				}
			case err != nil:
				t.Fatalf("resolveImage: %v", err)
			}
			if tt.wantInErr != "" && !strings.Contains(err.Error(), tt.wantInErr) {
				t.Errorf("error %q does not contain %q", err, tt.wantInErr)
			}
			if got != tt.want {
				t.Errorf("resolveImage = %q, want %q", got, tt.want)
			}
		})
	}
}

// The first-boot create path, against a strict runtime holding a
// containerd-store image: the container is made from the daemon's own ID, and
// the repo@config-digest reference the old code built would not have resolved.
func TestReconcileOnce_CreatesFromTheRecordedImageID(t *testing.T) {
	ctx := context.Background()

	for _, tt := range []struct {
		name   string
		image  container.ImageInfo
		wantID string
	}{
		{name: "containerd store", image: containerdImage, wantID: imgManifestDigest},
		{name: "classic store", image: classicImage, wantID: imgConfigDigest},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fake := container.NewFake()
			fake.StrictImages = true
			fake.AddImage(tt.image)
			if _, err := fake.ImageInspect(ctx, imgRepo+"@"+imgConfigDigest); err == nil {
				t.Fatal("the fake resolves repo@config-digest; it would not catch the original bug")
			}

			store := &fakeStore{}
			store.set(imageRow(imgConfigDigest))
			r, err := New(Config{
				Runtime: fake, Store: store, Subnets: testAllocator(t),
				Posture: container.PostureRootlessPodman, Interval: time.Hour,
				Images: recordWithLocalID(tt.wantID),
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			for i := 0; i < 3; i++ {
				result, err := r.ReconcileOnce(ctx)
				if err != nil {
					t.Fatalf("ReconcileOnce: %v", err)
				}
				if result.Errors != 0 {
					t.Fatalf("pass %d reported %d errors", i, result.Errors)
				}
			}

			created, err := fake.ListByLabel(ctx, LabelInstance, "i1")
			if err != nil {
				t.Fatalf("ListByLabel: %v", err)
			}
			if len(created) != 1 {
				t.Fatalf("%d containers created, want 1", len(created))
			}
			if created[0].Image != tt.wantID {
				t.Errorf("container image = %q, want the runtime-local ID %q", created[0].Image, tt.wantID)
			}
			if created[0].Labels[LabelImageDigest] != imgConfigDigest {
				t.Errorf("digest label = %q, want the pin %q (drift detection compares config digests)",
					created[0].Labels[LabelImageDigest], imgConfigDigest)
			}
		})
	}
}

func TestReconcileOnce_MissingImageCreatesNothing(t *testing.T) {
	ctx := context.Background()
	fake := container.NewFake()
	fake.StrictImages = true // nothing loaded

	store := &fakeStore{}
	store.set(imageRow(imgConfigDigest))
	r, err := New(Config{
		Runtime: fake, Store: store, Subnets: testAllocator(t),
		Posture: container.PostureRootlessPodman, Interval: time.Hour,
		Images: recordWithLocalID(imgManifestDigest),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var sawError bool
	for i := 0; i < 3; i++ {
		result, err := r.ReconcileOnce(ctx)
		if err != nil {
			t.Fatalf("ReconcileOnce: %v", err)
		}
		sawError = sawError || result.Errors > 0
	}
	if !sawError {
		t.Error("no pass reported the missing image as an error")
	}
	created, err := fake.ListByLabel(ctx, LabelInstance, "i1")
	if err != nil {
		t.Fatalf("ListByLabel: %v", err)
	}
	if len(created) != 0 {
		t.Errorf("%d containers created for an image that is not present, want 0", len(created))
	}
}

// Rotation create: the new generation's container comes from the recorded ID,
// and an absent image fails the step without burning the generation's token, so
// the same generation is created once the image is back.
func TestReconcileRotations_CreateFromRecordedImageID(t *testing.T) {
	ctx := context.Background()

	store, err := db.Open(filepath.Join(t.TempDir(), "rot.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	fake := container.NewFake()
	rt := &countingRuntime{Runtime: fake}
	r, err := New(Config{
		Runtime: rt, Store: store.Queries(), Rotations: store.Queries(), Images: store.Queries(),
		Subnets: testAllocator(t), Posture: container.PostureRootlessPodman, Interval: time.Hour,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	const (
		oldDigest = "sha256:aaa"
		newDigest = "sha256:bbb"
		newLocal  = "sha256:manifest-bbb"
	)
	seedInstance(t, store, rt, "inst-1", oldDigest, "cfg-1")
	fake.StrictImages = true // after seeding: the seed container's image is not under test
	setDesiredDigest(t, store, "inst-1", newDigest)

	now := time.Now().UTC().Format(time.RFC3339Nano)
	local := newLocal
	if _, err := store.Queries().UpsertContainerImage(ctx, db.UpsertContainerImageParams{
		Digest: newDigest, Reference: "ghcr.io/acme/p@" + newDigest, LoadedAt: now, LocalImageID: &local,
	}); err != nil {
		t.Fatalf("UpsertContainerImage: %v", err)
	}

	pass := func() RotationPassResult {
		t.Helper()
		result, err := r.ReconcileRotations(ctx)
		if err != nil {
			t.Fatalf("ReconcileRotations: %v", err)
		}
		return result
	}

	if got := pass(); got.Errors != 0 { // begin
		t.Fatalf("begin pass reported %d errors", got.Errors)
	}
	if got := pass(); got.Errors != 1 { // create, image absent
		t.Fatalf("create pass with no image reported %d errors, want 1", got.Errors)
	}
	if rt.creates != 1 { // only the seed container
		t.Fatalf("%d creates, want just the seed container", rt.creates)
	}

	fake.AddImage(container.ImageInfo{ID: newLocal})
	if got := pass(); got.Errors != 0 {
		t.Fatalf("create pass after the image returned reported %d errors", got.Errors)
	}

	infos, err := fake.ListByLabel(ctx, LabelGeneration, "2")
	if err != nil {
		t.Fatalf("ListByLabel: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("%d generation-2 containers, want 1", len(infos))
	}
	if infos[0].Image != newLocal {
		t.Errorf("generation 2 image = %q, want the recorded ID %q", infos[0].Image, newLocal)
	}
}
