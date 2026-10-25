package api

// sharetmp_test.go B5: GET /api/share/dl 响应须回传每个作业的转存文件清单
// files[{path, filename}](前端签名直下 /api/share/dl/url 用)。

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"baidupcs-web/internal/sharetmp"
)

// shareOrcStub 编排器桩: Preview/StartDownload 记录, List 返回预置作业
// (含转存产物 files), 供 HandleListDL 响应整形断言。
type shareOrcStub struct {
	orc     sharetmp.Orchestrator
	jobs    []sharetmp.Job
	started string
	fsids   []string
}

func (s *shareOrcStub) Preview(link, passcode string) (*sharetmp.Job, error) {
	return s.orc.Preview(link, passcode)
}
func (s *shareOrcStub) StartDownload(jobID string, fsids []string) error {
	s.started, s.fsids = jobID, fsids
	return nil
}
func (s *shareOrcStub) Cancel(jobID string) error { return s.orc.Cancel(jobID) }
func (s *shareOrcStub) List() []sharetmp.Job      { return s.jobs }

// TestHandleListDLFiles B5: 响应 list[].files 含 path/filename;
// 真实 orch 装配下 downloading 相位回传转存产物路径。
func TestHandleListDLFiles(t *testing.T) {
	realOrc, err := sharetmp.New(sharetmp.Deps{
		PreviewFn: func(context.Context, string, string) ([]sharetmp.Item, string, error) {
			return []sharetmp.Item{{FsID: "1", Filename: "a.bin"}}, "sid-1", nil
		},
		TransferFn: func(context.Context, string, string, []string) (string, []sharetmp.Item, error) {
			return "/来自分享/.tmp-sid-1",
				[]sharetmp.Item{{FsID: "1", Path: "/来自分享/.tmp-sid-1/a.bin", Filename: "a.bin"}}, nil
		},
		RemoveFn: func(context.Context, []string) error { return nil },
		DownloadFn: func(ctx context.Context, _ string) error {
			<-ctx.Done() // 阻塞在 downloading 相位, 供轮询断言
			return ctx.Err()
		},
		DeleteDelay: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	stub := &shareOrcStub{orc: realOrc}
	h := NewShareTmp(stub)

	j, err := realOrc.Preview("http://x", "1")
	if err != nil {
		t.Fatal(err)
	}
	if err := realOrc.StartDownload(j.ID, []string{"1"}); err != nil {
		t.Fatal(err)
	}
	// 轮询至 downloading 相位(Files 已回填)
	deadline := time.Now().Add(2 * time.Second)
	var gotJob sharetmp.Job
	for time.Now().Before(deadline) {
		for _, jj := range realOrc.List() {
			if jj.ID == j.ID {
				gotJob = jj
			}
		}
		if gotJob.Phase == sharetmp.PhaseDownloading {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if gotJob.Phase != sharetmp.PhaseDownloading {
		t.Fatalf("未达 downloading: %+v", gotJob)
	}
	stub.jobs = []sharetmp.Job{gotJob}

	rec := httptest.NewRecorder()
	h.HandleListDL(rec, httptest.NewRequest("GET", "/api/share/dl", nil))
	var body struct {
		Total int `json:"total"`
		List  []struct {
			ID    string `json:"id"`
			Phase string `json:"phase"`
			Files []struct {
				Path     string `json:"path"`
				Filename string `json:"filename"`
			} `json:"files"`
		} `json:"list"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("响应非 JSON: %v", err)
	}
	if body.Total != 1 || len(body.List) != 1 {
		t.Fatalf("应 1 个作业: %+v", body)
	}
	fl := body.List[0].Files
	if len(fl) != 1 || fl[0].Path != "/来自分享/.tmp-sid-1/a.bin" || fl[0].Filename != "a.bin" {
		t.Fatalf("files 应含转存产物 path/filename: %+v", fl)
	}
}
