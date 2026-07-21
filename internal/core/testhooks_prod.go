//go:build !cockpit_test

package core

func testBarrier(*daemon, string)        {}
func testMetadataDisabled(*daemon) bool  { return false }
func testCrashAfterEffect() bool         { return false }
func testDriverAmbiguous(*daemon) bool   { return false }
func testReadbackAmbiguous(*daemon) bool { return false }
func testStoreActor(string) string       { return "" }
