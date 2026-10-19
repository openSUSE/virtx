/*
 * Copyright (c) 2026 SUSE LLC
 *
 * This program is free software; you can redistribute it and/or
 * modify it under the terms of the GNU General Public License
 * as published by the Free Software Foundation; either version 2
 * of the License, or (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program; if not, see
 * <https://www.gnu.org/licenses/>
 */

/*
 * The wiring that makes this package's time windows follow the sanlock tier,
 * and the startup self-check that refuses to run when they do not.
 *
 * The values themselves live next to the code that uses them (DEAD_CONFIRM_SECONDS
 * in verdict.go, T_TAKEOVER_MS in action.go); here is the one place that takes
 * the effective tier from main, derives them again for the log, and verifies
 * that what the code uses is what the tier implies.
 */
package verdict

import (
	"fmt"

	"suse.com/virtx/pkg/lockman"
)

/*
 * tier is the effective sanlock tier this process was given. Until main hands
 * the real one over it is the documented default, so that a package used
 * without main - a unit test, for instance - computes what a default
 * deployment computes rather than a zero value.
 */
var tier = lockman.Default_tier()

/*
 * Set_tier applies the effective sanlock tier to the windows this package owns:
 * DEAD_CONFIRM_SECONDS follows io_timeout, and T_TAKEOVER_MS deliberately
 * follows nothing (it is the reset margin; see action.go). main calls it once,
 * before the judgement loop is started, so that no running goroutine ever sees
 * a half-updated window.
 */
func Set_tier(t lockman.Tier) {
	tier = t
	DEAD_CONFIRM_SECONDS = dead_confirm_seconds(t)
}

/*
 * Window_lines is what the startup log prints for the windows this package
 * owns: each value, the derivation that produced it, and the source of the
 * numbers it is derived from. The log line is meant to be readable on its own -
 * "which window, how was it computed, where did the inputs come from".
 */
func Window_lines() []string {
	return []string{
		fmt.Sprintf("window: DEAD_CONFIRM=%d s (RENEW_MS = %d x io_timeout; io_timeout=%d s source=%s)",
			DEAD_CONFIRM_SECONDS, T_RENEW_IO_MULTIPLIER, tier.Io_timeout_seconds, tier.Io_source),
		fmt.Sprintf("window: T_TAKEOVER=%d ms (the reset margin; the clock is the first DEAD reading, by which time DEAD_CONFIRM_MS=%d s and the watchdog are already past, so io_timeout=%d s source=%s does not enter it)",
			T_TAKEOVER_MS, tier.Dead_confirm_seconds(), tier.Io_timeout_seconds, tier.Io_source),
	}
}

/*
 * Check_sanlock_tier_windows is the startup self-check: it re-derives every window this
 * package uses from the tier it was given and refuses to run when the value the
 * code uses is not the value the tier implies. It exists because that is
 * exactly how these windows went wrong the first time - a literal stayed behind
 * when the cluster's tier changed, and the comment describing it kept saying a
 * number that had never been true here. A unit test pins the same property;
 * this check also covers a binary built from a dirty working tree.
 *
 * A failure is fatal on purpose: a window that does not match the tier is a
 * wrong safety margin, and the fail-closed answer is not to start with it.
 */
func Check_sanlock_tier_windows() error {
	var (
		want int = dead_confirm_seconds(tier)
	)
	if (DEAD_CONFIRM_SECONDS != want) {
		return fmt.Errorf("verdict: DEAD_CONFIRM_SECONDS=%d, but the tier (io_timeout=%d s, source=%s) derives %d",
			DEAD_CONFIRM_SECONDS, tier.Io_timeout_seconds, tier.Io_source, want)
	}
	if (T_TAKEOVER_MS != T_RESET_MARGIN_MS) {
		return fmt.Errorf("verdict: T_TAKEOVER_MS=%d, but it must be the reset margin=%d ms (the clock starts at the first DEAD reading)",
			T_TAKEOVER_MS, T_RESET_MARGIN_MS)
	}
	return nil
}
