// Package bazelcas provides strongly-typed abstractions and directory traversals
// for navigating Bazel's local output base and repository cache structures.
//
// Bazel organizes its build metadata, output artifacts, and external downloaded
// files under a hashed structure representing system user isolation and workspace
// directory absolute paths. This package models these concepts as immutable
// string-based types to ensure compile-time safety when performing path operations.
//
// # Path Hierarchy
//
// The path types form a three-tier hierarchy corresponding to Bazel's filesystem layout:
//   - [RootCASPath]: Represents the base cache directory (typically $HOME/.cache/bazel).
//   - [UserCASPath]: Represents the system user's root base (typically .../_bazel_[username]).
//   - [WorkspaceCASPath]: Represents a project workspace output base (typically .../[workspace_md5]).
//
// Navigating between these paths is done using helper methods that encapsulate
// parent-child relationships (e.g., resolving a [WorkspaceCASPath] from a [UserCASPath]).
//
// # Usage Example
//
//	import "github.com/meta-programming/bazelmop/pkg/bazelcas"
//
//	// Instantiate the root cache path
//	root := bazelcas.RootCASPath("/home/red/.cache/bazel")
//
//	// Resolve the user-specific output root
//	userRoot := root.User("red")
//	fmt.Println("User Root:", userRoot.String()) // /home/red/.cache/bazel/_bazel_red
//
//	// Access the repository cache CAS path
//	repoCAS := userRoot.RepositoryCache()
//	fmt.Println("Repo CAS:", repoCAS) // /home/red/.cache/bazel/_bazel_red/cache/repos/v1/content_addressable
//
//	// Access a specific workspace output base
//	workspaceBase := userRoot.Workspace("206ef5d7...")
//	fmt.Println("Workspace bazel-out:", workspaceBase.BazelOut()) // .../206ef5d7.../bazel-out
//
// # Concurrency and Thread Safety
//
// Since all path types are defined as string under the hood, they are immutable
// and completely safe for concurrent use across multiple go-routines without locking.
package bazelcas
