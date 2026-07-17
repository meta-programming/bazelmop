package bazelcas

import (
	"encoding/hex"
	"io/fs"
	"path"
	"strings"
)

// RootCASPath represents the root Bazel cache directory (e.g., $HOME/.cache/bazel).
type RootCASPath string

// String returns the clean string representation of the root path.
func (r RootCASPath) String() string {
	return string(r)
}

// User resolves the user-specific output root (UserCASPath).
func (r RootCASPath) User(username string) UserCASPath {
	return UserCASPath(path.Join(string(r), "_bazel_"+username))
}

// UserCASPath represents the user's output base root (e.g., .../_bazel_[username]).
type UserCASPath string

// String returns the clean string representation of the user path.
func (u UserCASPath) String() string {
	return string(u)
}

// Parent returns the RootCASPath of this UserCASPath.
func (u UserCASPath) Parent() RootCASPath {
	return RootCASPath(path.Dir(string(u)))
}

// RepositoryCache resolves the active CAS repository cache directory (RepoCASPath).
func (u UserCASPath) RepositoryCache() RepoCASPath {
	return RepoCASPath(path.Join(string(u), "cache", "repos", "v1", "content_addressable"))
}

// Workspace resolves the specific workspace output base (WorkspaceCASPath) by its MD5 hash.
func (u UserCASPath) Workspace(md5 string) WorkspaceCASPath {
	return WorkspaceCASPath(path.Join(string(u), md5))
}

// WorkspaceCASPath represents the output base directory for a specific workspace.
type WorkspaceCASPath string

// String returns the clean string representation of the workspace path.
func (w WorkspaceCASPath) String() string {
	return string(w)
}

// Parent returns the UserCASPath of this WorkspaceCASPath.
func (w WorkspaceCASPath) Parent() UserCASPath {
	return UserCASPath(path.Dir(string(w)))
}

// ExecRoot resolves the execroot directory.
func (w WorkspaceCASPath) ExecRoot() string {
	return path.Join(string(w), "execroot")
}

// BazelOut resolves the bazel-out directory.
func (w WorkspaceCASPath) BazelOut() string {
	return path.Join(string(w), "bazel-out")
}

// External resolves the external repository dependency directory.
func (w WorkspaceCASPath) External() string {
	return path.Join(string(w), "external")
}

// RepoCASPath represents the root of the content-addressable storage (CAS)
// directory within Bazel's repository cache.
type RepoCASPath string

// HashDir resolves the path to the directory containing a specific content hash.
func (r RepoCASPath) HashDir(algo, hash string) HashDirPath {
	return HashDirPath(path.Join(string(r), algo, hash))
}

// HashDirPath represents a directory containing a single content-addressed
// archive or dependency file inside the Bazel CAS.
type HashDirPath string

// File returns the absolute path to the actual cached dependency file.
func (h HashDirPath) File() string {
	return path.Join(string(h), "file")
}

// TempFile returns the path to a temporary download file inside the same directory.
func (h HashDirPath) TempFile(suffix string) string {
	return path.Join(string(h), "tmp-"+suffix)
}

// CacheTree represents the parsed in-memory structure of the Bazel cache.
type CacheTree struct {
	users map[string]*UserCache
}

// Users returns the map of usernames to their parsed UserCaches.
func (c *CacheTree) Users() map[string]*UserCache {
	return c.users
}

// UserCache represents the output user root and cache files for a specific system user.
type UserCache struct {
	username        string
	userPath        UserCASPath
	workspaces      map[string]*WorkspaceCache
	repositoryCache *RepoCache
}

// Username returns the user's username.
func (u *UserCache) Username() string {
	return u.username
}

// UserPath returns the strongly-typed UserCASPath.
func (u *UserCache) UserPath() UserCASPath {
	return u.userPath
}

// Workspaces returns the map of workspace MD5s to WorkspaceCaches.
func (u *UserCache) Workspaces() map[string]*WorkspaceCache {
	return u.workspaces
}

// RepositoryCache returns the RepoCache metadata representation.
func (u *UserCache) RepositoryCache() *RepoCache {
	return u.repositoryCache
}

// RepoCache represents the metadata of the user's Repository Cache (CAS).
type RepoCache struct {
	path   RepoCASPath
	hashes []string
}

// Path returns the RepoCASPath.
func (r *RepoCache) Path() RepoCASPath {
	return r.path
}

// Hashes returns the list of SHA-256 content hashes found in the Repository Cache.
func (r *RepoCache) Hashes() []string {
	return r.hashes
}

// WorkspaceCache represents a discovered workspace output base directory structure.
type WorkspaceCache struct {
	md5           string
	path          WorkspaceCASPath
	externalRepos []string
}

// MD5 returns the workspace MD5.
func (w *WorkspaceCache) MD5() string {
	return w.md5
}

// Path returns the WorkspaceCASPath.
func (w *WorkspaceCache) Path() WorkspaceCASPath {
	return w.path
}

// ExternalRepos returns the names of any repositories discovered under the external/ folder.
func (w *WorkspaceCache) ExternalRepos() []string {
	return w.externalRepos
}

// isMD5 checks if a string is a 32-character hex MD5 hash.
func isMD5(s string) bool {
	if len(s) != 32 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// isSHA256 checks if a string is a 64-character hex SHA-256 hash.
func isSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// Walk walks the provided fs.FS starting at the FS root and builds the [CacheTree].
func Walk(sys fs.FS) (*CacheTree, error) {
	tree := &CacheTree{
		users: make(map[string]*UserCache),
	}

	// 1. Walk the FS root looking for _bazel_[username] directories
	entries, err := fs.ReadDir(sys, ".")
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "_bazel_") {
			continue
		}

		username := strings.TrimPrefix(entry.Name(), "_bazel_")
		userPathStr := entry.Name()
		userCASPath := UserCASPath(userPathStr)

		userCache := &UserCache{
			username:   username,
			userPath:   userCASPath,
			workspaces: make(map[string]*WorkspaceCache),
		}
		tree.users[username] = userCache

		// 2. Discover repository cache
		repoCachePathRel := path.Join(userPathStr, "cache", "repos", "v1", "content_addressable")
		repoInfo, err := fs.Stat(sys, repoCachePathRel)
		if err == nil && repoInfo.IsDir() {
			repoCache := &RepoCache{
				path: RepoCASPath(repoCachePathRel),
			}
			userCache.repositoryCache = repoCache

			// Walk sha256 directory inside repo cache
			sha256PathRel := path.Join(repoCachePathRel, "sha256")
			shaEntries, err := fs.ReadDir(sys, sha256PathRel)
			if err == nil {
				for _, shaEntry := range shaEntries {
					if shaEntry.IsDir() && isSHA256(shaEntry.Name()) {
						// Check if "file" exists inside
						filePath := path.Join(sha256PathRel, shaEntry.Name(), "file")
						if fileInfo, err := fs.Stat(sys, filePath); err == nil && !fileInfo.IsDir() {
							repoCache.hashes = append(repoCache.hashes, shaEntry.Name())
						}
					}
				}
			}
		}

		// 3. Discover workspaces (MD5 named folders in the user dir)
		userEntries, err := fs.ReadDir(sys, userPathStr)
		if err != nil {
			continue
		}

		for _, userEntry := range userEntries {
			if !userEntry.IsDir() || !isMD5(userEntry.Name()) {
				continue
			}

			workspacePathRel := path.Join(userPathStr, userEntry.Name())
			workspaceCASPath := WorkspaceCASPath(workspacePathRel)

			// Verify it's a real workspace output base by checking for common folders
			hasExecroot := false
			hasBazelout := false
			hasExternal := false

			wsSubentries, err := fs.ReadDir(sys, workspacePathRel)
			if err == nil {
				for _, sub := range wsSubentries {
					if sub.IsDir() {
						switch sub.Name() {
						case "execroot":
							hasExecroot = true
						case "bazel-out":
							hasBazelout = true
						case "external":
							hasExternal = true
						}
					}
				}
			}

			// If it has at least some of the workspace base directories, count it
			if hasExecroot || hasBazelout || hasExternal {
				wsCache := &WorkspaceCache{
					md5:  userEntry.Name(),
					path: workspaceCASPath,
				}
				userCache.workspaces[userEntry.Name()] = wsCache

				// Scan external repositories if external/ exists
				if hasExternal {
					externalPathRel := path.Join(workspacePathRel, "external")
					extEntries, err := fs.ReadDir(sys, externalPathRel)
					if err == nil {
						for _, extEntry := range extEntries {
							if extEntry.IsDir() && !strings.HasPrefix(extEntry.Name(), "@") {
								wsCache.externalRepos = append(wsCache.externalRepos, extEntry.Name())
							}
						}
					}
				}
			}
		}
	}

	return tree, nil
}
