//go:build cockpit_test

package core

import (
	"os"
	"path/filepath"
	"time"
)

func testBarrier(d *daemon, name string) {
	if p := os.Getenv("COCKPIT_TEST_BARRIER_DIR"); p != "" {
		_ = os.WriteFile(filepath.Join(p, name), []byte("ack"), 0600)
	}
	hold := filepath.Join(d.root, "hold-"+name)
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if _, err := os.Stat(hold); os.IsNotExist(err) {
			return
		}
	}
}
func testMetadataDisabled(d *daemon) bool {
	_, err := os.Stat(filepath.Join(d.root, "disable-metadata"))
	return err == nil
}
func testCrashAfterEffect() bool { return os.Getenv("COCKPIT_TEST_CRASH_AFTER_EFFECT") == "1" }
func testDriverAmbiguous(d *daemon) bool {
	_, err := os.Stat(filepath.Join(d.root, "driver-ambiguous"))
	return err == nil
}
func testReadbackAmbiguous(d *daemon) bool {
	_, err := os.Stat(filepath.Join(d.root, "readback-ambiguous"))
	return err == nil
}
func testStoreActor(string) string { return os.Getenv("COCKPIT_TEST_ACTOR") }
