package views

import (
	"testing"
	"time"
)

func TestFormatTokens_KAndM(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{42, "42"},
		{999, "999"},
		{1000, "1.0k"},
		{12345, "12.3k"},
		{999_999, "1000.0k"}, // boundary just below 1M still in k-form
		{1_000_000, "1.0M"},
		{1_234_567, "1.2M"},
	}
	for _, c := range cases {
		got := FormatTokens(c.n)
		if got != c.want {
			t.Errorf("FormatTokens(%d) = %q want %q", c.n, got, c.want)
		}
	}
}

func TestFormatElapsed_SecondsMinutesHours(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{5 * time.Second, "5s"},
		{59 * time.Second, "59s"},
		{60 * time.Second, "1m 0s"},
		{91 * time.Second, "1m 31s"},
		{59*time.Minute + 59*time.Second, "59m 59s"},
		{60 * time.Minute, "1h 0m"},
		{3700 * time.Second, "1h 1m"},
		{2*time.Hour + 30*time.Minute, "2h 30m"},
	}
	for _, c := range cases {
		got := FormatElapsed(c.d)
		if got != c.want {
			t.Errorf("FormatElapsed(%v) = %q want %q", c.d, got, c.want)
		}
	}
}

func TestThinkingStatusText_RotationByElapsed(t *testing.T) {
	// Use a healthy tokens/sec so the "almost done" branch never
	// fires and we exercise the elapsed-only rotation.
	const healthyTPS = 50.0
	cases := []struct {
		elapsed time.Duration
		want    string
	}{
		{0, "thinking"},
		{4 * time.Second, "thinking"},
		{5 * time.Second, "cogitating"},
		{14 * time.Second, "cogitating"},
		{15 * time.Second, "still thinking"},
		{59 * time.Second, "still thinking"},
		{60 * time.Second, "cooking"},
		{10 * time.Minute, "cooking"},
	}
	for _, c := range cases {
		got := ThinkingStatusText(c.elapsed, healthyTPS)
		if got != c.want {
			t.Errorf("ThinkingStatusText(%v, %v) = %q want %q",
				c.elapsed, healthyTPS, got, c.want)
		}
	}
}

func TestThinkingStatusText_AlmostDoneWhenTokenRateLow(t *testing.T) {
	// Low TPS after the 3s warmup window flips us to "almost done"
	// regardless of which elapsed-bucket we'd otherwise be in.
	got := ThinkingStatusText(20*time.Second, 1.0)
	if got != "almost done" {
		t.Errorf("ThinkingStatusText(20s, 1.0 tps) = %q want \"almost done\"", got)
	}
	// But during the warmup (<= 3s) we ignore the low rate — the
	// stream just hasn't started producing tokens yet.
	if got := ThinkingStatusText(2*time.Second, 0.0); got != "thinking" {
		t.Errorf("ThinkingStatusText(2s, 0.0 tps) = %q want \"thinking\" (warmup)", got)
	}
}

func TestRenderSlider_FullWhenAtCap(t *testing.T) {
	got := RenderSlider(100, 10)
	want := "▰▰▰▰▰▰▰▰▰▰"
	if got != want {
		t.Errorf("RenderSlider(100, 10) = %q want %q", got, want)
	}
	// Above the cap clamps to full, doesn't overflow.
	if got := RenderSlider(500, 10); got != want {
		t.Errorf("RenderSlider(500, 10) = %q want %q (clamp)", got, want)
	}
}

func TestRenderSlider_ProportionalFill(t *testing.T) {
	// 30 t/s ÷ 100 cap = 0.3 → 3 of 10 cells filled.
	got := RenderSlider(30, 10)
	want := "▰▰▰▱▱▱▱▱▱▱"
	if got != want {
		t.Errorf("RenderSlider(30, 10) = %q want %q", got, want)
	}
	// 0 fill renders all empty.
	if got := RenderSlider(0, 10); got != "▱▱▱▱▱▱▱▱▱▱" {
		t.Errorf("RenderSlider(0, 10) = %q want all empty", got)
	}
	// Negative rate clamps to 0 (defensive — TokensPerSecond may
	// produce negative values briefly if usage decreased between
	// samples e.g. after a /clear).
	if got := RenderSlider(-50, 10); got != "▱▱▱▱▱▱▱▱▱▱" {
		t.Errorf("RenderSlider(-50, 10) = %q want all empty", got)
	}
}

func TestTokensPerSecond_FromTwoSamples(t *testing.T) {
	now := time.Now()
	samples := []UsageSample{
		{Ts: now.Add(-1 * time.Second), OutputTokens: 100},
		{Ts: now, OutputTokens: 150},
	}
	// 50 tokens / 1 second = 50 tps.
	got := TokensPerSecond(samples, now, 2*time.Second)
	if got < 49.9 || got > 50.1 {
		t.Errorf("TokensPerSecond = %v want ~50", got)
	}

	// One sample → 0.
	if got := TokensPerSecond(samples[:1], now, 2*time.Second); got != 0 {
		t.Errorf("TokensPerSecond(1 sample) = %v want 0", got)
	}

	// Empty → 0.
	if got := TokensPerSecond(nil, now, 2*time.Second); got != 0 {
		t.Errorf("TokensPerSecond(nil) = %v want 0", got)
	}

	// All samples outside the window → 0.
	old := []UsageSample{
		{Ts: now.Add(-10 * time.Second), OutputTokens: 0},
		{Ts: now.Add(-9 * time.Second), OutputTokens: 50},
	}
	if got := TokensPerSecond(old, now, 2*time.Second); got != 0 {
		t.Errorf("TokensPerSecond(stale samples) = %v want 0", got)
	}
}
