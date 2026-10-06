package sync

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ys-ll/uniterm/backend/log"
	"github.com/ys-ll/uniterm/backend/utils"
)

// syncSnapshot runs one sync cycle against a snapshot backend. The
// three-way base is the device's LastSyncedHash — the aggregate content
// hash both sides agreed on at the last successful sync. The up-to-date
// fast path is hash-first and downloads nothing; every other branch
// fetches the remote snapshot exactly once into a staging dir that is
// shared by the verify and pull steps. Callers must hold s.mu.
func (s *SyncService) syncSnapshot(b SnapshotBackend) (*SyncResult, error) {
	config, err := s.configStore.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	// effectiveFiles, not raw EffectiveSyncFiles: a broken scope list
	// falls back to the legacy set so sync can never become a silent
	// no-op.
	files := s.effectiveFiles()

	encKey, err := s.keychain.GetEncryptionKey()
	if err != nil {
		return nil, fmt.Errorf("encryption key: %w", err)
	}

	manifest, err := b.Head()
	if err != nil {
		s.updateLastSyncResult("failed", fmt.Sprintf("head: %v", err))
		return nil, fmt.Errorf("head: %w", err)
	}

	// Empty remote: publish local (bootstrap, or remote was wiped).
	// No fetch needed.
	if manifest == nil {
		return s.pushSnapshot(b, files, encKey, "")
	}

	// Hash first: the fast paths below must not download anything.
	_, localHash, err := hashConfigFiles(files, s.dataDir, s.keychain, s.passwordStore)
	if err != nil {
		s.updateLastSyncResult("failed", err.Error())
		return nil, err
	}
	remoteHash := aggregateHash(manifest.FileHashes)
	last := config.LastSyncedHash

	if last != "" && localHash == last && remoteHash == last {
		s.updateLastSyncResult("success", "")
		return &SyncResult{Message: "已是最新"}, nil
	}
	if last == "" && localHash == remoteHash {
		// Merge base lost (re-configure, migration) but both sides carry
		// identical content — re-anchor silently instead of surfacing a
		// spurious conflict (mirrors git mode's no-common-ancestor heal).
		s.setLastSyncedHash(remoteHash)
		s.updateLastSyncResult("success", "")
		return &SyncResult{Message: "已是最新"}, nil
	}

	// Something to reconcile — fetch the remote snapshot ONCE into a
	// staging dir and confirm this key can open the remote ciphertext
	// (SYNC-P1-11 snapshot edition) before anything is pushed or
	// decrypted over local data.
	stagingDir, err := os.MkdirTemp("", "snapshot-sync-")
	if err != nil {
		s.updateLastSyncResult("failed", fmt.Sprintf("staging dir: %v", err))
		return nil, fmt.Errorf("staging dir: %w", err)
	}
	defer os.RemoveAll(stagingDir)
	if err := b.FetchAll(stagingDir); err != nil {
		s.updateLastSyncResult("failed", fmt.Sprintf("fetch: %v", err))
		return nil, fmt.Errorf("fetch: %w", err)
	}
	if err := verifyDecryptionScoped(files, stagingDir, encKey); err != nil {
		s.updateLastSyncResult("password_mismatch", err.Error())
		return nil, err
	}

	switch {
	case last == "" && isConfigDirEmpty(files, s.dataDir):
		// First device joining with no local data → adopt remote.
		return s.pullSnapshot(files, stagingDir, encKey, remoteHash)

	case last != "" && localHash == last && remoteHash != last:
		// Only remote changed → silent pull.
		return s.pullSnapshot(files, stagingDir, encKey, remoteHash)

	case last != "" && remoteHash == last && localHash != last:
		// Only local changed → push.
		return s.pushSnapshot(b, files, encKey, stagingDir)

	default:
		// Both changed (or first join with local data) → conflict dialog.
		s.updateLastSyncResult("conflict", "")
		return &SyncResult{
			Direction: SyncConflict,
			Conflict: &ConflictInfo{
				LocalTime:  getConfigModTime(s.dataDir),
				RemoteTime: manifest.UpdatedAt,
			},
		}, nil
	}
}

// pullSnapshot decrypts the already-fetched remote snapshot (stagingDir)
// into dataDir. Only files the remote actually carries are decrypted:
// writing "{}" over out-of-manifest local files would silently wipe them
// when another device syncs with a narrower scope. Callers must hold s.mu.
func (s *SyncService) pullSnapshot(files []string, stagingDir string, encKey []byte, remoteHash string) (*SyncResult, error) {
	var present []string
	for _, name := range files {
		if _, err := os.Stat(filepath.Join(stagingDir, name)); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			s.updateLastSyncResult("failed", fmt.Sprintf("stat %s: %v", name, err))
			return nil, fmt.Errorf("stat %s: %w", name, err)
		}
		present = append(present, name)
	}
	if err := DecryptConfigFilesScoped(present, stagingDir, s.dataDir, encKey, s.passwordStore); err != nil {
		s.updateLastSyncResult("failed", fmt.Sprintf("decrypt files: %v", err))
		return nil, fmt.Errorf("decrypt files: %w", err)
	}
	s.setLastSyncedHash(remoteHash)
	s.updateLastSyncResult("success", "")
	return &SyncResult{Direction: SyncPull, Message: "配置已下载"}, nil
}

// pushSnapshot encrypts the scoped local config into the staging dir and
// uploads it as a new remote snapshot. fetchedDir is the already-fetched
// remote staging dir ("" on bootstrap, where no fetch happened) — its
// .sync-salt is reused so the repo keeps one salt identity across pushes.
// Callers must hold s.mu.
func (s *SyncService) pushSnapshot(b SnapshotBackend, files []string, encKey []byte, fetchedDir string) (*SyncResult, error) {
	hashes, agg, err := hashConfigFiles(files, s.dataDir, s.keychain, s.passwordStore)
	if err != nil {
		s.updateLastSyncResult("failed", err.Error())
		return nil, err
	}

	// Salt: reuse the remote's (same repo identity across pushes — read
	// BEFORE the staging rebuild, because EncryptConfigFilesScoped does
	// not write the salt file and the rebuild below wipes the dir);
	// generate on bootstrap or when no usable salt exists.
	var salt []byte
	if fetchedDir != "" {
		salt, err = ReadSaltFile(fetchedDir)
		if err != nil {
			log.Writef("snapshot sync: read remote salt: %v", err)
			salt = nil
		}
	}

	// Rebuild staging: only this scope's ciphertext + salt.
	if err := os.RemoveAll(s.repoPath); err != nil {
		s.updateLastSyncResult("failed", fmt.Sprintf("clear staging: %v", err))
		return nil, fmt.Errorf("clear staging: %w", err)
	}
	if err := os.MkdirAll(s.repoPath, 0755); err != nil {
		s.updateLastSyncResult("failed", fmt.Sprintf("create staging: %v", err))
		return nil, fmt.Errorf("create staging: %w", err)
	}
	if err := EncryptConfigFilesScoped(files, s.dataDir, s.repoPath, encKey, s.keychain, s.passwordStore); err != nil {
		s.updateLastSyncResult("failed", fmt.Sprintf("encrypt files: %v", err))
		return nil, fmt.Errorf("encrypt files: %w", err)
	}

	if len(salt) == 0 {
		salt, err = GenerateSalt()
		if err != nil {
			s.updateLastSyncResult("failed", fmt.Sprintf("generate salt: %v", err))
			return nil, fmt.Errorf("generate salt: %w", err)
		}
	}
	if err := WriteSaltFile(s.repoPath, salt); err != nil {
		s.updateLastSyncResult("failed", fmt.Sprintf("write salt: %v", err))
		return nil, fmt.Errorf("write salt: %w", err)
	}

	// Unique per upload (RFC3339Nano + random suffix) so the post-push
	// re-Head below can actually detect a lost update.
	now := time.Now().UTC()
	version, err := newSnapshotVersion()
	if err != nil {
		s.updateLastSyncResult("failed", fmt.Sprintf("version suffix: %v", err))
		return nil, fmt.Errorf("version suffix: %w", err)
	}
	if err := b.StoreAll(s.repoPath, salt, SnapshotInfo{
		Version:    version,
		UpdatedAt:  now,
		FileHashes: hashes,
	}); err != nil {
		s.updateLastSyncResult("failed", fmt.Sprintf("store: %v", err))
		return nil, fmt.Errorf("store: %w", err)
	}

	// Lost-update check: another device may have overwritten the snapshot
	// between our Head and StoreAll. Best-effort — a re-Head failure never
	// fails an already-completed push.
	head, err := b.Head()
	if err != nil {
		log.Writef("snapshot sync: re-head after push: %v", err)
	} else if head != nil && head.Version != version {
		// Our upload was clobbered within the window: do not record the
		// agreed hash — surface the conflict dialog instead of silently
		// losing the local changes.
		s.updateLastSyncResult("conflict", "")
		return &SyncResult{
			Direction: SyncConflict,
			Conflict: &ConflictInfo{
				LocalTime:  getConfigModTime(s.dataDir),
				RemoteTime: head.UpdatedAt,
			},
		}, nil
	}

	s.setLastSyncedHash(agg)
	s.updateLastSyncResult("success", "")
	return &SyncResult{Direction: SyncPush, Message: "配置已上传"}, nil
}

// randomHex returns n crypto/rand bytes hex-encoded, used to make snapshot
// versions unique across same-tick uploads.
func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// newSnapshotVersion returns a unique per-upload snapshot version —
// RFC3339Nano + a random hex suffix — so the post-push re-Head can detect
// a lost update even for same-tick uploads.
func newSnapshotVersion() (string, error) {
	suffix, err := randomHex(4)
	if err != nil {
		return "", err
	}
	return time.Now().UTC().Format(time.RFC3339Nano) + "-" + suffix, nil
}

// resolveConflictSnapshot resolves a snapshot conflict by direction.
// useLocal re-publishes local content (reusing the remote salt so the
// repo keeps one identity); useRemote adopts the remote snapshot
// wholesale. Callers must hold s.mu.
func (s *SyncService) resolveConflictSnapshot(b SnapshotBackend, useLocal bool) (*SyncResult, error) {
	files := s.effectiveFiles()
	encKey, err := s.keychain.GetEncryptionKey()
	if err != nil {
		return nil, fmt.Errorf("encryption key: %w", err)
	}

	manifest, err := b.Head()
	if err != nil {
		return nil, fmt.Errorf("head: %w", err)
	}
	if manifest == nil {
		// Remote wiped between the conflict and the resolve: the only
		// content left is local — bootstrap push.
		return s.pushSnapshot(b, files, encKey, "")
	}

	// Fetch the remote snapshot into the shared staging dir: pushSnapshot
	// reads the remote salt from it (repo identity) and pullSnapshot
	// decrypts the ciphertext out of it.
	if err := os.RemoveAll(s.repoPath); err != nil {
		return nil, fmt.Errorf("clean staging: %w", err)
	}
	if err := b.FetchAll(s.repoPath); err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}

	if useLocal {
		return s.pushSnapshot(b, files, encKey, s.repoPath)
	}
	return s.pullSnapshot(files, s.repoPath, encKey, aggregateHash(manifest.FileHashes))
}

// setLastSyncedHash records the agreed content hash after a successful
// snapshot sync. Load→mutate→Save keeps every other SyncConfig field
// (SyncScope, AutoSync, …) intact. Best-effort persistence like
// updateLastSyncResult.
func (s *SyncService) setLastSyncedHash(hash string) {
	config, err := s.configStore.Load()
	if err != nil {
		log.Writef("snapshot sync: load config for hash: %v", err)
		return
	}
	config.LastSyncedHash = hash
	if err := s.configStore.Save(config); err != nil {
		log.Writef("snapshot sync: save hash: %v", err)
	}
}

// verifyDecryptionScoped checks key against every scoped ciphertext file
// present in dir. Returns nil when none exist (fresh remote).
func verifyDecryptionScoped(files []string, dir string, key []byte) error {
	for _, name := range files {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if _, err := decryptBytes(string(data), key, name); err != nil {
			return fmt.Errorf("verify %s: %w", name, err)
		}
	}
	return nil
}

// ConfigureRepoWebDAV sets up (or adopts) a WebDAV snapshot share. An
// existing remote snapshot is adopted: its salt is the repo identity, the
// master password is verified against its ciphertext, and the local side
// either pulls (no local data), records the agreed hash (identical), or
// surfaces the conflict dialog (both sides have data). A fresh remote
// generates a new salt and publishes the local config as the initial
// snapshot.
func (s *SyncService) ConfigureRepoWebDAV(serverURL, basePath, username, password, masterPassword string) (*SyncResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.requireUnlocked(); err != nil {
		return nil, err
	}

	// Carry over this device's existing scope/auto-sync (empty on first
	// configure → legacy default set). Part A regression: every
	// config-save site must preserve AutoSync and SyncScope.
	existing, _ := s.configStore.Load()
	files := s.effectiveFiles()

	if err := s.keychain.SetWebDAVPassword(password); err != nil {
		return nil, fmt.Errorf("store webdav password: %w", err)
	}
	backend := NewWebDAVBackend(serverURL, basePath, username, password, files)
	if err := backend.Probe(); err != nil {
		return nil, fmt.Errorf("probe webdav: %w", err)
	}

	// Fresh staging for the fetch below.
	if err := os.RemoveAll(s.repoPath); err != nil {
		return nil, fmt.Errorf("clean staging: %w", err)
	}
	if err := os.MkdirAll(s.repoPath, 0755); err != nil {
		return nil, fmt.Errorf("create staging: %w", err)
	}

	manifest, err := backend.Head()
	if err != nil {
		return nil, fmt.Errorf("head: %w", err)
	}

	// Part A pattern: the fresh struct's zero value clears LastSyncedHash —
	// a new backend is a new three-way base.
	saveCfg := func() error {
		return s.configStore.Save(SyncConfig{
			Backend:      "webdav",
			WebDAVServer: serverURL,
			WebDAVPath:   basePath,
			WebDAVUser:   username,
			Branch:       "main",
			AutoSync:     existing.AutoSync,
			SyncScope:    existing.SyncScope,
		})
	}

	if manifest != nil {
		// Remote already has a snapshot: verify the master password
		// against its ciphertext before anything is stored or decrypted.
		if err := backend.FetchAll(s.repoPath); err != nil {
			return nil, fmt.Errorf("fetch: %w", err)
		}
		salt, err := ReadSaltFile(s.repoPath)
		if err != nil {
			return nil, err
		}
		if salt == nil {
			return nil, utils.UserErr("salt_missing")
		}
		encKey := DeriveKey(masterPassword, salt)
		if err := verifyDecryptionScoped(files, s.repoPath, encKey); err != nil {
			return nil, utils.UserErr("master_password_mismatch")
		}
		if err := s.keychain.StoreEncryptionKey(encKey); err != nil {
			return nil, fmt.Errorf("store encryption key: %w", err)
		}

		tmpDir, err := os.MkdirTemp("", "webdav-compare-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(tmpDir)
		if err := DecryptConfigFilesScoped(files, s.repoPath, tmpDir, encKey, nil); err != nil {
			return nil, fmt.Errorf("decrypt remote for comparison: %w", err)
		}

		if err := saveCfg(); err != nil {
			return nil, err
		}

		if isConfigDirEmpty(files, s.dataDir) {
			// Local empty → adopt the remote wholesale.
			if _, err := s.pullSnapshot(files, s.repoPath, encKey, aggregateHash(manifest.FileHashes)); err != nil {
				return nil, err
			}
			return &SyncResult{Direction: SyncPull, Message: "仓库配置成功，已从远端同步配置"}, nil
		}
		same, err := compareConfigDirs(files, s.dataDir, tmpDir, s.keychain, s.passwordStore)
		if err != nil {
			return nil, fmt.Errorf("compare configs: %w", err)
		}
		if !same {
			s.updateLastSyncResult("conflict", "")
			return &SyncResult{
				Direction: SyncConflict,
				Message:   "本地和远端配置不一致，请选择覆盖方向",
				Conflict: &ConflictInfo{
					LocalTime:  getConfigModTime(s.dataDir),
					RemoteTime: manifest.UpdatedAt,
				},
			}, nil
		}
		// Content identical: record the agreed hash as the new base.
		_, agg, err := hashConfigFiles(files, s.dataDir, s.keychain, s.passwordStore)
		if err != nil {
			return nil, err
		}
		s.setLastSyncedHash(agg)
		s.updateLastSyncResult("success", "")
		return &SyncResult{Message: "仓库配置成功"}, nil
	}

	// Fresh remote: generate salt, derive key, publish local config.
	salt, err := GenerateSalt()
	if err != nil {
		return nil, fmt.Errorf("generate salt: %w", err)
	}
	encKey := DeriveKey(masterPassword, salt)
	if err := s.keychain.StoreEncryptionKey(encKey); err != nil {
		return nil, fmt.Errorf("store encryption key: %w", err)
	}
	if err := saveCfg(); err != nil {
		return nil, err
	}
	// Seed the staging dir with the new salt and pass it as the fetched
	// dir: pushSnapshot reads the fetchedDir salt BEFORE its staging
	// rebuild, so the published remote salt matches the key derivation.
	// Without this, bootstrap would publish a second random salt and
	// re-attaching with the same master password would fail.
	if err := WriteSaltFile(s.repoPath, salt); err != nil {
		return nil, fmt.Errorf("write salt: %w", err)
	}
	if _, err := s.pushSnapshot(backend, files, encKey, s.repoPath); err != nil {
		return nil, err
	}
	return &SyncResult{Direction: SyncPush, Message: "仓库配置成功"}, nil
}

// UpdateWebDAVPassword swaps the WebDAV credential without touching any
// other configuration. Mirrors the git credential-edit semantics: an empty
// password means "keep the stored one" — it is verified against the server
// but never rewritten. A non-empty password is probed against the server
// first, and when the remote already holds a snapshot the master password
// must decrypt it — a typo can neither lock the device out of its own
// snapshot nor silently save a broken credential. Changing server/path/
// user is intentionally not possible here: that is a different sync
// target, which must go through delete + re-configure.
func (s *SyncService) UpdateWebDAVPassword(password, masterPassword string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.requireUnlocked(); err != nil {
		return err
	}
	config, err := s.configStore.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if config.Backend != "webdav" || config.WebDAVServer == "" {
		return fmt.Errorf("webdav sync not configured")
	}
	if masterPassword == "" {
		return fmt.Errorf("master password is required")
	}

	// Empty password = keep the stored credential (git-token semantics).
	store := password != ""
	if !store {
		password, _ = s.keychain.GetWebDAVPassword()
		if password == "" {
			return fmt.Errorf("webdav password not configured")
		}
	}

	backend := NewWebDAVBackend(config.WebDAVServer, config.WebDAVPath, config.WebDAVUser, password, nil)
	if err := backend.Probe(); err != nil {
		return fmt.Errorf("probe webdav: %w", err)
	}

	manifest, err := backend.Head()
	if err != nil {
		return fmt.Errorf("head: %w", err)
	}
	if manifest != nil {
		if err := os.RemoveAll(s.repoPath); err != nil {
			return fmt.Errorf("clean staging: %w", err)
		}
		if err := os.MkdirAll(s.repoPath, 0755); err != nil {
			return fmt.Errorf("create staging: %w", err)
		}
		if err := backend.FetchAll(s.repoPath); err != nil {
			return fmt.Errorf("fetch: %w", err)
		}
		salt, err := ReadSaltFile(s.repoPath)
		if err != nil {
			return err
		}
		if salt == nil {
			return utils.UserErr("salt_missing")
		}
		encKey := DeriveKey(masterPassword, salt)
		if err := verifyDecryptionScoped(s.effectiveFiles(), s.repoPath, encKey); err != nil {
			return utils.UserErr("master_password_mismatch")
		}
	}

	if store {
		if err := s.keychain.SetWebDAVPassword(password); err != nil {
			return fmt.Errorf("store webdav password: %w", err)
		}
	}
	return nil
}

// changePasswordSnapshot rotates the master password of a WebDAV snapshot.
// The rotation is repo-global (the salt is repo-global): the remote
// ciphertext is decrypted with the old key, re-encrypted with a fresh salt
// + new key, and uploaded. The local keychain key is only swapped after
// the upload succeeds, so a failed rotation leaves the local key (and the
// remote) decryptable with the old password.
func (s *SyncService) changePasswordSnapshot(oldPassword, newPassword string) error {
	config, err := s.configStore.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	files := s.effectiveFiles()
	password, _ := s.keychain.GetWebDAVPassword()
	backend := NewWebDAVBackend(config.WebDAVServer, config.WebDAVPath, config.WebDAVUser, password, files)

	manifest, err := backend.Head()
	if err != nil {
		return fmt.Errorf("head: %w", err)
	}
	if manifest == nil {
		return utils.UserErr("salt_missing")
	}

	// Fetch the current snapshot and verify the old password opens it.
	if err := os.RemoveAll(s.repoPath); err != nil {
		return fmt.Errorf("clean staging: %w", err)
	}
	if err := os.MkdirAll(s.repoPath, 0755); err != nil {
		return fmt.Errorf("create staging: %w", err)
	}
	if err := backend.FetchAll(s.repoPath); err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	oldSalt, err := ReadSaltFile(s.repoPath)
	if err != nil {
		return err
	}
	if oldSalt == nil {
		return utils.UserErr("salt_missing")
	}
	oldKey := DeriveKey(oldPassword, oldSalt)
	if err := verifyDecryptionScoped(files, s.repoPath, oldKey); err != nil {
		return utils.UserErr("current_password_mismatch")
	}

	// Old ciphertext → plaintext, staged in a temp dir.
	tmpDir, err := os.MkdirTemp("", "webdav-rotate-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)
	if err := DecryptConfigFilesScoped(files, s.repoPath, tmpDir, oldKey, nil); err != nil {
		return fmt.Errorf("decrypt: %w", err)
	}

	// New salt/key; re-encrypt the plaintext into staging.
	newSalt, err := GenerateSalt()
	if err != nil {
		return fmt.Errorf("generate salt: %w", err)
	}
	newKey := DeriveKey(newPassword, newSalt)
	if err := os.RemoveAll(s.repoPath); err != nil {
		return fmt.Errorf("clean staging: %w", err)
	}
	if err := os.MkdirAll(s.repoPath, 0755); err != nil {
		return fmt.Errorf("create staging: %w", err)
	}
	if err := EncryptConfigFilesScoped(files, tmpDir, s.repoPath, newKey, nil, nil); err != nil {
		return fmt.Errorf("re-encrypt: %w", err)
	}
	if err := WriteSaltFile(s.repoPath, newSalt); err != nil {
		return fmt.Errorf("write salt: %w", err)
	}
	hashes, agg, err := hashConfigFiles(files, tmpDir, nil, nil)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	version, err := newSnapshotVersion()
	if err != nil {
		return fmt.Errorf("version suffix: %w", err)
	}
	// Upload first, then swap the local key: a StoreAll failure must leave
	// the local keychain key matching the still-old remote.
	if err := backend.StoreAll(s.repoPath, newSalt, SnapshotInfo{
		Version:    version,
		UpdatedAt:  now,
		FileHashes: hashes,
	}); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	if err := s.keychain.StoreEncryptionKey(newKey); err != nil {
		return fmt.Errorf("store new key: %w", err)
	}
	s.setLastSyncedHash(agg)
	return nil
}
