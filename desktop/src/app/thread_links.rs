//! A thread's linked requests (THI-103), shown the way GitHub shows an issue's
//! cross-references: under the opening message, one row per other request,
//! saying how it is linked, with its reference, title and status. A row opens
//! that request's thread.
//!
//! The board derives them (internal/board/links.go): explicit links (related,
//! or duplicate when a request was merged into another), and mentions of
//! KEY-n in any message. Each link shows on both threads.
use super::*;
use crate::board::Related;

/// The label of a linked request's row.
pub(super) fn link_label(r: &Related) -> &'static str {
    match (r.kind.as_str(), r.incoming) {
        ("duplicate", false) => "link_duplicate_of",
        ("duplicate", true) => "link_merged_here",
        ("mention", false) => "link_mentions",
        ("mention", true) => "link_mentioned_in",
        _ => "link_related",
    }
}

/// Rows in the order they show: a merge first, then the rest as the board
/// lists them (explicit links, then mentions; outgoing before incoming).
pub(super) fn ordered(related: &[Related]) -> Vec<&Related> {
    let mut rows: Vec<&Related> = related.iter().collect();
    rows.sort_by_key(|r| r.kind != "duplicate");
    rows
}

impl FennecWindow {
    pub(super) fn thread_links(
        &self,
        p: &Project,
        r: &Request,
        cx: &mut Context<Self>,
    ) -> Option<AnyElement> {
        if r.related.is_empty() {
            return None;
        }
        let rows = ordered(&r.related)
            .into_iter()
            .enumerate()
            .map(|(i, link)| {
                let n = link.number;
                h_flex()
                    .id(SharedString::from(format!("thread-link-{i}")))
                    .h(px(30.0))
                    .px(px(8.0))
                    .gap(px(8.0))
                    .rounded(px(6.0))
                    .cursor_pointer()
                    .hover(|s| s.bg(c("f_line").opacity(0.5)))
                    .on_click(
                        cx.listener(move |this, _, window, cx| this.open_request(n, window, cx)),
                    )
                    .child(
                        div()
                            .flex_none()
                            .w(px(96.0))
                            .truncate()
                            .text_size(px(11.5))
                            .text_color(c("f_muted"))
                            .child(t(link_label(link))),
                    )
                    .child(
                        div()
                            .flex_none()
                            .text_size(px(12.0))
                            .text_color(c("f_muted"))
                            .child(format!("{}-{}", p.key, n)),
                    )
                    .child(
                        div()
                            .flex_1()
                            .min_w_0()
                            .truncate()
                            .text_size(px(12.5))
                            .text_color(c("f_text"))
                            .child(link.title.clone()),
                    )
                    .child(ui::status_pill(&link.status))
                    .into_any_element()
            });
        Some(
            v_flex()
                .id("thread-links")
                .mb(px(6.0))
                .pb(px(10.0))
                .border_b_1()
                .border_color(c("f_line"))
                .child(
                    div()
                        .px(px(8.0))
                        .pb(px(4.0))
                        .text_size(px(11.5))
                        .font_weight(FontWeight::MEDIUM)
                        .text_color(c("f_muted"))
                        .child(t("linked_requests")),
                )
                .children(rows)
                .into_any_element(),
        )
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn link(n: u64, kind: &str, incoming: bool) -> Related {
        Related {
            number: n,
            kind: kind.into(),
            incoming,
            ..Default::default()
        }
    }

    #[test]
    fn a_merge_leads_and_each_direction_has_its_label() {
        let related = vec![
            link(1, "related", false),
            link(2, "mention", true),
            link(3, "duplicate", true),
        ];
        let rows = ordered(&related);
        assert_eq!(rows.iter().map(|r| r.number).collect::<Vec<_>>(), [3, 1, 2]);
        assert_eq!(link_label(rows[0]), "link_merged_here");
        assert_eq!(
            link_label(&link(4, "duplicate", false)),
            "link_duplicate_of"
        );
        assert_eq!(link_label(rows[2]), "link_mentioned_in");
        assert_eq!(link_label(&link(5, "mention", false)), "link_mentions");
        assert_eq!(link_label(rows[1]), "link_related");
    }

    #[test]
    fn a_thread_reads_its_links_from_the_board() {
        let r: Request = serde_json::from_value(serde_json::json!({
            "number": 7,
            "related": [{"number": 3, "title": "Sync stalls", "status": "resolved", "kind": "duplicate"}]
        }))
        .unwrap();
        assert_eq!(
            r.related,
            vec![Related {
                number: 3,
                title: "Sync stalls".into(),
                status: "resolved".into(),
                kind: "duplicate".into(),
                incoming: false
            }]
        );
    }
}
