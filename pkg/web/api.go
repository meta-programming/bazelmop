package web

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	bazelmopv1 "github.com/meta-programming/bazelmop/gen/bazelmop/v1"
	"github.com/meta-programming/bazelmop/gen/bazelmop/v1/bazelmopv1connect"
	"github.com/meta-programming/bazelmop/pkg/bazelcas"
)

// apiServer implements the daemon's Connect API over a [Server]'s state.
type apiServer struct {
	bazelmopv1connect.UnimplementedBazelmopServiceHandler
	s *Server
}

func (a *apiServer) GetStatus(ctx context.Context, req *connect.Request[bazelmopv1.GetStatusRequest]) (*connect.Response[bazelmopv1.GetStatusResponse], error) {
	return connect.NewResponse(&bazelmopv1.GetStatusResponse{Status: a.s.statusProto()}), nil
}

func (a *apiServer) WatchStatus(ctx context.Context, req *connect.Request[bazelmopv1.WatchStatusRequest], stream *connect.ServerStream[bazelmopv1.WatchStatusResponse]) error {
	changed, stop := a.s.watch()
	defer stop()
	for {
		if err := stream.Send(&bazelmopv1.WatchStatusResponse{Status: a.s.statusProto()}); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-changed:
		}
	}
}

func (a *apiServer) ListOutputBases(ctx context.Context, req *connect.Request[bazelmopv1.ListOutputBasesRequest]) (*connect.Response[bazelmopv1.ListOutputBasesResponse], error) {
	a.s.mu.RLock()
	insts := a.s.instances
	a.s.mu.RUnlock()

	res := &bazelmopv1.ListOutputBasesResponse{}
	for _, inst := range insts {
		res.OutputBases = append(res.OutputBases, outputBaseProto(inst))
	}
	return connect.NewResponse(res), nil
}

func (a *apiServer) DeleteOutputBases(ctx context.Context, req *connect.Request[bazelmopv1.DeleteOutputBasesRequest]) (*connect.Response[bazelmopv1.DeleteOutputBasesResponse], error) {
	res := &bazelmopv1.DeleteOutputBasesResponse{}
	for _, id := range req.Msg.GetIds() {
		if err := a.s.deleteOutputBase(id); err != nil {
			res.Failures = append(res.Failures, &bazelmopv1.DeleteFailure{Id: id, Message: err.Error()})
			continue
		}
		res.DeletedIds = append(res.DeletedIds, id)
	}
	return connect.NewResponse(res), nil
}

func (s *Server) statusProto() *bazelmopv1.DaemonStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	activity := s.status
	if activity == "" {
		activity = "Idle"
	}
	return &bazelmopv1.DaemonStatus{
		Activity:              activity,
		ReportTime:            timestampOrNil(s.updatedAt),
		NextRunTime:           timestampOrNil(s.nextScanAt),
		ReportMarkdown:        s.reportMarkdown,
		OutputBasesUpdateTime: timestampOrNil(s.instancesUpdatedAt),
	}
}

func outputBaseProto(inst *bazelcas.Instance) *bazelmopv1.OutputBase {
	ob := &bazelmopv1.OutputBase{
		Id:               inst.ID,
		Path:             inst.Path,
		WorkspaceExists:  inst.Status == bazelcas.StatusActive,
		SizeBytes:        inst.SizeBytes,
		FileCount:        inst.ItemCount,
		LastModifiedTime: timestampOrNil(inst.LastModified),
		LastBuildTime:    timestampOrNil(inst.LastBuildTime),
		LastTestTime:     timestampOrNil(inst.LastTestTime),
	}
	switch inst.Type {
	case bazelcas.TypeOutputBase:
		ob.Kind = bazelmopv1.OutputBaseKind_OUTPUT_BASE_KIND_OUTPUT_BASE
		if inst.WorkspacePath != "N/A" {
			ob.WorkspacePath = inst.WorkspacePath
		}
	case bazelcas.TypeRepoCache:
		ob.Kind = bazelmopv1.OutputBaseKind_OUTPUT_BASE_KIND_REPOSITORY_CACHE
	}
	return ob
}

func timestampOrNil(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}
