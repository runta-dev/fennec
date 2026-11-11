package uninstall

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Plan struct {
	ResumeID    string       `json:"resume_id,omitempty"`
	ID          string       `json:"id"`
	Local       LocalPlan    `json:"local"`
	Cloud       *CloudTarget `json:"cloud,omitempty"`
	DeleteCloud bool         `json:"delete_cloud"`
	SourceBuild string       `json:"source_build"`
	Expires     time.Time    `json:"expires"`
}
type Record struct {
	WorkerPID int `json:"worker_pid,omitempty"`
	WorkerPath string `json:"worker_path,omitempty"`
	Plan         Plan    `json:"plan"`
	Request      Request `json:"request"`
	Stage        string  `json:"stage"`
	Error        string  `json:"error,omitempty"`
	ErrorCode    string  `json:"error_code,omitempty"`
	CloudDeleted bool    `json:"cloud_deleted"`
}

func Marker(home string) string {
	return filepath.Join(home, ".local/state/fennec-uninstall/active.json")
}
func Pending(home string) bool { _, err := os.Lstat(Marker(home)); return !os.IsNotExist(err) }
func ReadRecord(path string) (Record, error) {
	var r Record
	info, err := os.Lstat(path)
	if err != nil {
		return r, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 1<<20 {
		return r, fmt.Errorf("private uninstall record required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return r, err
	}
	if json.Unmarshal(data, &r) != nil || r.Plan.ID == "" {
		return r, fmt.Errorf("invalid uninstall record")
	}
	return r, nil
}
func WriteRecord(path string, r Record) error {
	if r.Plan.ID == "" {
		return fmt.Errorf("uninstall plan identity required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".uninstall-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), path)
}
func ConfirmPlan(plan Plan, request Request, now time.Time) error {
	if plan.ID == "" || !now.Before(plan.Expires) {
		return fmt.Errorf("uninstall preview expired; review it again")
	}
	if request.DeleteCloud != plan.DeleteCloud || !request.ConfirmLocal {
		return fmt.Errorf("confirm the displayed uninstall plan")
	}
	if request.DeleteCloud && (plan.Cloud == nil || plan.Cloud.ID == "" || plan.Cloud.Owner == "" || request.ConfirmRuntime != plan.Cloud.ID) {
		return fmt.Errorf("type the cloud computer ID to confirm deletion")
	}
	return nil
}
