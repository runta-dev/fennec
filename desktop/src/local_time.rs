//! Board times in the user's own time zone (THI-119).
//!
//! The board stores RFC 3339 times in UTC (`2026-11-25T07:30:57.553Z`).
//! Everything the desktop shows goes through here so a user in Singapore
//! sees 15:30, not 07:30. A string that doesn't parse is shown as is.

use chrono::{DateTime, Local, TimeZone};

fn parse<Tz: TimeZone>(at: &str, tz: &Tz) -> Option<DateTime<Tz>> {
    DateTime::parse_from_rfc3339(at).ok().map(|t| t.with_timezone(tz))
}

fn format_in<Tz: TimeZone>(at: &str, tz: &Tz, fmt: &str) -> String
where
    Tz::Offset: std::fmt::Display,
{
    parse(at, tz).map_or_else(|| at.to_owned(), |t| t.format(fmt).to_string())
}

/// `HH:MM`, for a thread message.
pub fn clock(at: &str) -> String {
    format_in(at, &Local, "%H:%M")
}

/// `MM-DD HH:MM`, for a mention row.
pub fn month_day_clock(at: &str) -> String {
    format_in(at, &Local, "%m-%d %H:%M")
}

/// `YYYY-MM-DD HH:MM`, for a knowledge entry.
pub fn stamp(at: &str) -> String {
    format_in(at, &Local, "%Y-%m-%d %H:%M")
}

/// `YYYY-MM-DD`, the local date.
pub fn day(at: &str) -> String {
    format_in(at, &Local, "%Y-%m-%d")
}

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::FixedOffset;

    fn sgt() -> FixedOffset {
        FixedOffset::east_opt(8 * 3600).unwrap()
    }

    #[test]
    fn shows_utc_times_in_the_local_zone() {
        let at = "2026-11-25T07:30:57.553474Z";
        assert_eq!(format_in(at, &sgt(), "%H:%M"), "15:30");
        assert_eq!(format_in(at, &sgt(), "%m-%d %H:%M"), "11-25 15:30");
        // Crossing midnight moves the date too.
        let late = "2026-11-25T20:05:00Z";
        assert_eq!(format_in(late, &sgt(), "%Y-%m-%d %H:%M"), "2026-11-26 04:05");
        assert_eq!(format_in(late, &sgt(), "%Y-%m-%d"), "2026-11-26");
        // An explicit offset is honored, not assumed UTC.
        assert_eq!(format_in("2026-11-25T15:30:00+08:00", &sgt(), "%H:%M"), "15:30");
    }

    #[test]
    fn leaves_unparseable_text_alone() {
        assert_eq!(format_in("", &sgt(), "%H:%M"), "");
        assert_eq!(format_in("yesterday", &sgt(), "%H:%M"), "yesterday");
    }
}
