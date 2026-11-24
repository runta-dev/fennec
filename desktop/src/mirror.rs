//! herdr-app's `mirror::qualify`, which `links.rs` calls: a terminal address on
//! another machine is `<machine>/<id>`. (herdr-app src/mirror.rs, Apache-2.0.)

pub fn qualify(id: &str, machine: Option<&str>) -> String {
    machine.map_or_else(|| id.to_owned(), |m| format!("{m}/{id}"))
}
