// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package symbolication

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

const deviceBundle = "/Users/me/Library/Developer/CoreSimulator/Devices/0CC7E3AE/data/Containers/Data/Application/C695AE80/Library/Application Support/.expo-internal/cc6bcf262eb4e628381054e969f59f70.bundle"

func TestParseFrame(t *testing.T) {
	for _, tc := range []struct {
		line string
		want Frame
	}{
		{"    at LabScreen (address at " + deviceBundle + ":1:1129430)", Frame{Kind: BytecodeFrame, Function: "LabScreen", File: deviceBundle, Line: 1, Column: 1129430}},
		{"    at anonymous (address at InternalBytecode.js:1:9)", Frame{Kind: BytecodeFrame, Function: "anonymous", File: "InternalBytecode.js", Line: 1, Column: 9}},
		{"    at onPress (http://10.0.2.2:8081/index.bundle?platform=ios:12:5)", Frame{Kind: SourceFrame, Function: "onPress", File: "http://10.0.2.2:8081/index.bundle?platform=ios", Line: 12, Column: 5}},
		{"    at async Object.load (src/cart.js:3:11)", Frame{Kind: SourceFrame, Function: "async Object.load", File: "src/cart.js", Line: 3, Column: 11}},
		{"    at forEach (native)", Frame{Kind: NativeFrame, Function: "forEach"}},
		{"    at src/cart.js:3:11", Frame{Kind: SourceFrame, File: "src/cart.js", Line: 3, Column: 11}},
		{"addItem@main.jsbundle:1:4012", Frame{Kind: SourceFrame, Function: "addItem", File: "main.jsbundle", Line: 1, Column: 4012}},
		{"@https://app.example.com/main.js:10:2", Frame{Kind: SourceFrame, File: "https://app.example.com/main.js", Line: 10, Column: 2}},
		{"forEach@[native code]", Frame{Kind: NativeFrame, Function: "forEach"}},
		{"com.facebook.react.bridge.JavaMethodWrapper.invoke(JavaMethodWrapper.java:372)", Frame{Kind: NativeFrame, Function: "com.facebook.react.bridge.JavaMethodWrapper.invoke", File: "JavaMethodWrapper.java", Line: 372}},
		{"android.os.Looper.loop(Native Method)", Frame{Kind: NativeFrame, Function: "android.os.Looper.loop", File: "Native Method"}},
		{"-[RCTCxxBridge invokeModule] (React + 482112)", Frame{Kind: NativeFrame, Function: "-[RCTCxxBridge invokeModule]", File: "React"}},
		{"libsystem_kernel.dylib + 4012", Frame{Kind: NativeFrame, File: "libsystem_kernel.dylib"}},
		{"0x1a2b3c", Frame{Kind: NativeFrame}},
	} {
		got, ok := ParseFrame(tc.line)
		if assert.True(t, ok, tc.line) {
			assert.Equal(t, tc.want, got, tc.line)
		}
	}
}

func TestParseFrameRefusesText(t *testing.T) {
	for _, line := range []string{
		"Error: Deliberate async crash from the observe lab",
		"Caused by: java.lang.IllegalStateException: boom",
		"The cart has 3 items",
		"",
		"    at " + strings.Repeat("x", maxFrameLineBytes) + " (a.js:1:2)",
	} {
		_, ok := ParseFrame(line)
		assert.False(t, ok, line)
	}
}

func TestSkippedFrames(t *testing.T) {
	count, ok := SkippedFrames("    ... skipping 42 frames")
	assert.True(t, ok)
	assert.Equal(t, 42, count)

	count, ok = SkippedFrames("… +7 more frames")
	assert.True(t, ok)
	assert.Equal(t, 7, count)

	count, ok = SkippedFrames(SkippedFramesLine(220))
	assert.True(t, ok)
	assert.Equal(t, 220, count)

	_, ok = SkippedFrames("    at f (a.js:1:2)")
	assert.False(t, ok)
}
