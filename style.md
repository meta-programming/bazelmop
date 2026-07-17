# Go Style and Coding Guidelines

This document outlines the coding standards, style requirements, and documentation guidelines for the `bazel-cache-share` project. All Go source code must strictly adhere to these standards.

## 1. Style Guide

This project follows the **Uber Go Style Guide** as its base coding standard.
- **Reference**: [Uber Go Style Guide](https://github.com/uber-go/guide/blob/master/style.md)
- **Key tenets**:
  - Avoid global variables and global state.
  - Keep interfaces small and focused.
  - Return early to reduce nesting.
  - Handle errors immediately rather than wrapping everything in deep conditionals.
  - Use `goimports` and `gofmt` to format all code.

---

## 2. Documentation Guidelines (Go Docstrings)

We follow the modern Go doc formatting standards introduced in Go 1.19, including support for headings, lists, and links.
- **Reference**: [Go Doc Comments Specification](https://go.dev/doc/comment)

### 2.1. Documenting Public Symbols
All exported (public) symbols—including packages, constants, variables, functions, methods, structs, and interfaces—**must** be documented with a clear, context-providing doc comment.

- Doc comments must start with the name of the symbol being declared.
- They must explain the purpose of the symbol, its inputs, and its outputs, without restating the signature verbatim.

Example:
```go
// Deduplicator walks Bazel directories and replaces duplicate files
// with space-saving reflinks or hard links.
//
// For more details on the filesystem mechanics, see the design spec:
// [Design spec](file:///home/red/ws/bazel-cache-share/design.md)
type Deduplicator struct {
    // ...
}
```

### 2.2. Package-Level Documentation
Every package must contain a package-level doc comment that provides high-level context, architecture, and usage examples.
- For multi-file packages, this doc comment should reside in a file named `doc.go` or directly above the `package` declaration in the package's primary entrypoint file.
- The comment must explain:
  - What the package does.
  - Its core components and how they interact.
  - Important concurrency, lifecycle, or safety guarantees.

Example:
```go
// Package dedupe provides filesystem walk, byte-by-byte matching,
// and atomic file link replacement capabilities for Bazel caches.
//
// It is designed to run safely concurrent with active Bazel builds by
// executing all links atomically.
package dedupe
```

### 2.3. Link Support in Docstrings
Leverage Go 1.19+ link definitions at the bottom of the doc comment to keep the text readable.

```go
// Match performs byte-by-byte checks on files of identical sizes.
// It groups identical files into [EquivalenceClass] partitions.
//
// See [Linux ioctl] for how reflinks are cloned.
//
// [Linux ioctl]: https://man7.org/linux/man-pages/man2/ioctl_ficlonerange.2.html
func (d *Deduplicator) Match(ctx context.Context) error
```
