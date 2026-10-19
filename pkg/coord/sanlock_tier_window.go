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
 * The value itself lives next to the code that uses it (T_COORD_HANDOFF_MS in
 * coord.go); here is the one place that takes the effective tier from main,
 * derives the window again for the log, and verifies that what the code uses is
 * what the tier implies.
 */
package coord

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
 * Set_tier applies the effective sanlock tier to T_COORD_HANDOFF_MS. main calls
 * it once, before the patrol is started, so that no running goroutine ever sees
 * a half-updated window.
 */
func Set_tier(t lockman.Tier) {
	tier = t
	T_COORD_HANDOFF_MS = coord_handoff_ms(t)
}

/*
 * Window_lines is what the startup log prints for the window this package owns:
 * the value, the derivation that produced it term by term, and the source of
 * the tier it is derived from.
 */
func Window_lines() []string {
	return []string{
		fmt.Sprintf("window: T_COORD_HANDOFF=%d ms (MASK_WINDOW_MS = HANDOFF_DETECT_MS %d + DEAD_CONFIRM_MS %d + the mask margin %d = %d s; DEAD_CONFIRM_MS = %d x io_timeout %d s source=%s + watchdog_fire_timeout %d s source=%s)",
			T_COORD_HANDOFF_MS, T_HANDOFF_DETECT_SECONDS, tier.Dead_confirm_seconds(), T_MASK_DELTA_SECONDS,
			T_COORD_HANDOFF_MS / 1000, lockman.DEAD_CONFIRM_IO_MULTIPLIER,
			tier.Io_timeout_seconds, tier.Io_source,
			tier.Watchdog_fire_timeout_seconds, tier.Watchdog_source),
	}
}

/*
 * Check_sanlock_tier_windows is the startup self-check: it re-derives T_COORD_HANDOFF_MS
 * from the tier it was given and refuses to run when the value the code uses is
 * not the value the tier implies. It exists because that is exactly how this
 * window went wrong the first time: the literal was right for the 10/60 tier,
 * but its own comment described a range instead of a number and named no tier,
 * so a reader could not tell which tier it belonged to. A unit test pins the
 * same property; this check also covers a binary built from a dirty tree.
 *
 * A failure is fatal on purpose: a handoff window that does not match the tier
 * is a wrong safety margin, and the fail-closed answer is not to start with it.
 */
func Check_sanlock_tier_windows() error {
	var (
		want int64 = coord_handoff_ms(tier)
	)
	if (T_COORD_HANDOFF_MS != want) {
		return fmt.Errorf("coord: T_COORD_HANDOFF_MS=%d, but the tier (io_timeout=%d s source=%s, watchdog_fire_timeout=%d s source=%s) derives %d",
			T_COORD_HANDOFF_MS, tier.Io_timeout_seconds, tier.Io_source,
			tier.Watchdog_fire_timeout_seconds, tier.Watchdog_source, want)
	}
	return nil
}
