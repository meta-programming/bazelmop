package web

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	bazelmopv1 "github.com/meta-programming/bazelmop/gen/bazelmop/v1"
	"github.com/meta-programming/bazelmop/gen/bazelmop/v1/bazelmopv1connect"
	"github.com/meta-programming/bazelmop/pkg/bazelcas"
	"github.com/meta-programming/bazelmop/pkg/dedupe"
	"github.com/meta-programming/bazelmop/pkg/runlog"
	"github.com/meta-programming/bazelmop/pkg/sysstat"
)

// statusRefresh is how often WatchStatus sends a status when nothing has
// changed, so memory and disk figures stay current.
const statusRefresh = 5 * time.Second

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
	refresh := time.NewTicker(statusRefresh)
	defer refresh.Stop()
	for {
		if err := stream.Send(&bazelmopv1.WatchStatusResponse{Status: a.s.statusProto()}); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-changed:
		case <-refresh.C:
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

func (a *apiServer) ListRuns(ctx context.Context, req *connect.Request[bazelmopv1.ListRunsRequest]) (*connect.Response[bazelmopv1.ListRunsResponse], error) {
	res := &bazelmopv1.ListRunsResponse{}
	if a.s.runLog == nil {
		return connect.NewResponse(res), nil
	}
	limit := int(req.Msg.GetLimit())
	if limit <= 0 {
		limit = 20
	}
	runs, err := a.s.runLog.Recent(limit)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	for _, r := range runs {
		res.Runs = append(res.Runs, runProto(r))
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
	activity := s.status
	if activity == "" {
		activity = "Idle"
	}
	st := &bazelmopv1.DaemonStatus{
		Activity:              activity,
		ReportTime:            timestampOrNil(s.updatedAt),
		NextRunTime:           timestampOrNil(s.nextScanAt),
		ReportMarkdown:        s.reportMarkdown,
		OutputBasesUpdateTime: timestampOrNil(s.instancesUpdatedAt),
		Phase:                 phaseProto(s.progress.Phase),
		PhaseDone:             s.progress.Done,
		PhaseTotal:            s.progress.Total,
		RunStartTime:          timestampOrNil(s.runStart),
		PhaseStartTime:        timestampOrNil(s.phaseStart),
	}
	s.mu.RUnlock()

	if m, err := sysstat.SelfMemory(); err == nil {
		st.Memory = &bazelmopv1.MemoryUsage{
			CurrentBytes: m.Current,
			HighBytes:    m.High,
			MaxBytes:     m.Max,
			AnonBytes:    m.Anon,
			FileBytes:    m.File,
			KernelBytes:  m.Kernel,
			HighEvents:   m.HighEvents,
		}
	}
	if s.root != "" {
		if d, err := sysstat.DiskUsage(s.root); err == nil {
			st.Disk = &bazelmopv1.DiskUsage{Path: s.root, TotalBytes: d.Total, FreeBytes: d.Free}
		}
	}
	return st
}

func phaseProto(p dedupe.Phase) bazelmopv1.RunPhase {
	switch p {
	case dedupe.PhaseIdle:
		return bazelmopv1.RunPhase_RUN_PHASE_IDLE
	case dedupe.PhaseScan:
		return bazelmopv1.RunPhase_RUN_PHASE_SCAN
	case dedupe.PhaseReadExtents:
		return bazelmopv1.RunPhase_RUN_PHASE_READ_EXTENTS
	case dedupe.PhaseHash:
		return bazelmopv1.RunPhase_RUN_PHASE_HASH
	case dedupe.PhaseLink:
		return bazelmopv1.RunPhase_RUN_PHASE_LINK
	}
	return bazelmopv1.RunPhase_RUN_PHASE_UNSPECIFIED
}

func runProto(r runlog.Run) *bazelmopv1.Run {
	out := &bazelmopv1.Run{
		Id:               r.ID,
		StartTime:        timestampOrNil(r.Start),
		EndTime:          timestampOrNil(r.End),
		Error:            r.Error,
		FilesScanned:     r.FilesScanned,
		FilesHashed:      r.FilesHashed,
		LinksCreated:     r.LinksCreated,
		LinksFailed:      r.LinksFailed,
		ApparentBytes:    r.ApparentBytes,
		SharedBytes:      r.SharedBytes,
		ReclaimableBytes: r.ReclaimableBytes,
		PeakMemoryBytes:  r.PeakMemoryBytes,
		DiskFreeBytes:    r.DiskFreeBytes,
	}
	switch r.Outcome {
	case runlog.Running:
		out.Outcome = bazelmopv1.RunOutcome_RUN_OUTCOME_RUNNING
	case runlog.Completed:
		out.Outcome = bazelmopv1.RunOutcome_RUN_OUTCOME_COMPLETED
	case runlog.Failed:
		out.Outcome = bazelmopv1.RunOutcome_RUN_OUTCOME_FAILED
	case runlog.Interrupted:
		out.Outcome = bazelmopv1.RunOutcome_RUN_OUTCOME_INTERRUPTED
	}
	return out
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
