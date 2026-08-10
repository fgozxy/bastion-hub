package targets

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// GitHub's Contents API rejects files well below its nominal 100 MB cap — this
// repo's threshold sits between 32 and 40 MiB (the base64 PUT body hits a ~50 MB
// wall). 24 MiB → ~32 MB body, a comfortable margin. OneDrive uses resumable
// upload sessions (no per-file cap) and VPS/SFTP has none, so only GitHub shards.
const githubShardSize int64 = 32 * 1024 * 1024

// shardManifest is stored at <remoteName>.manifest and records how many parts
// the object was split into. Pull reads it first so it knows the exact part
// names and total size without relying on List (which paginates / is capped).
type shardManifest struct {
	Shards int   `json:"shards"`
	Size   int64 `json:"size"`
}

// rawStore is the single-file primitive interface a backend supplies; sharding
// is layered on top so a backend with a per-file size cap can still store an
// arbitrarily large object.
type rawStore interface {
	putRaw(ctx context.Context, localPath, remoteName string) error
	getRaw(ctx context.Context, remoteName, localPath string) error
	delRaw(ctx context.Context, remoteName string) error
}

func partName(remoteName string, i int) string {
	return remoteName + ".p" + fmt.Sprintf("%06d", i)
}

func manifestName(remoteName string) string {
	return remoteName + ".manifest"
}

// shardPull reassembles remoteName from its parts into localPath, using the
// manifest. If no manifest exists it falls back to a single-file pull (so older
// non-sharded backups still restore).
func shardPull(ctx context.Context, rs rawStore, remoteName, localPath string) error {
	var m shardManifest
	if mb, ok := readManifest(ctx, rs, remoteName); ok {
		if err := json.Unmarshal(mb, &m); err != nil {
			return fmt.Errorf("shard pull manifest: %w", err)
		}
	} else {
		// no manifest → legacy single-file object
		return rs.getRaw(ctx, remoteName, localPath)
	}

	out, err := os.Create(localPath)
	if err != nil {
		return err
	}
	defer out.Close()

	partTmp, err := os.CreateTemp("", "np-part-*.bin")
	if err != nil {
		return err
	}
	partTmpName := partTmp.Name()
	partTmp.Close()
	defer os.Remove(partTmpName)

	var written int64
	for i := 0; i < m.Shards; i++ {
		if err := rs.getRaw(ctx, partName(remoteName, i), partTmpName); err != nil {
			return fmt.Errorf("shard pull part %d: %w", i, err)
		}
		pf, err := os.Open(partTmpName)
		if err != nil {
			return err
		}
		n, cerr := io.Copy(out, pf)
		pf.Close()
		if cerr != nil {
			return cerr
		}
		written += n
	}
	if m.Size > 0 && written != m.Size {
		return fmt.Errorf("shard pull size mismatch: got %d want %d", written, m.Size)
	}
	return nil
}

// readManifest fetches the manifest for remoteName. Returns (bytes, false) if
// there is no manifest (legacy single-file object) or it can't be read.
func readManifest(ctx context.Context, rs rawStore, remoteName string) ([]byte, bool) {
	tmp, err := os.CreateTemp("", "np-manifest-*.json")
	if err != nil {
		return nil, false
	}
	tmpName := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpName)
	if err := rs.getRaw(ctx, manifestName(remoteName), tmpName); err != nil {
		return nil, false
	}
	b, err := os.ReadFile(tmpName)
	if err != nil {
		return nil, false
	}
	return b, true
}
