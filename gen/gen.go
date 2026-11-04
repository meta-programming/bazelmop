// Package gen holds code generated from the protocol buffers in proto/.
//
// The generated files are checked in so that go install works without
// protoc. To regenerate them, install protoc, protoc-gen-go and
// protoc-gen-connect-go, then run go generate ./gen.
package gen

//go:generate protoc -I ../proto --go_out=. --go_opt=paths=source_relative --connect-go_out=. --connect-go_opt=paths=source_relative bazelmop/v1/bazelmop.proto
