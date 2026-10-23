//! render(svg) -> [w u32][h u32][premultiplied RGBA], for Go's image.RGBA as is.
use resvg::{tiny_skia, usvg};
use std::sync::Arc;

static mut FONTS: Option<usvg::fontdb::Database> = None;

#[allow(static_mut_refs)]
fn fonts() -> &'static mut usvg::fontdb::Database {
    unsafe { FONTS.get_or_insert_with(usvg::fontdb::Database::new) }
}

#[no_mangle]
pub extern "C" fn alloc(n: usize) -> *mut u8 {
    let mut v = Vec::<u8>::with_capacity(n);
    let p = v.as_mut_ptr();
    std::mem::forget(v);
    p
}

#[no_mangle]
pub unsafe extern "C" fn dealloc(p: *mut u8, n: usize) {
    drop(Vec::from_raw_parts(p, 0, n));
}

/// add_font: a TTF/OTF/TTC; the first one added is also the default family.
#[no_mangle]
pub unsafe extern "C" fn add_font(p: *const u8, n: usize) {
    let db = fonts();
    let first = db.is_empty();
    db.load_font_data(std::slice::from_raw_parts(p, n).to_vec());
    if first {
        let name = db.faces().next().and_then(|f| f.families.first().map(|(n, _)| n.clone()));
        if let Some(name) = name {
            db.set_sans_serif_family(name);
        }
    }
}

/// render: the SVG at its size (small ones scaled up to min_side, big ones down to max_side).
/// Returns ptr << 32 | len of the result, 0 when it can't be read.
#[no_mangle]
pub unsafe extern "C" fn render(p: *const u8, n: usize, min_side: u32, max_side: u32) -> u64 {
    let data = std::slice::from_raw_parts(p, n);
    let mut opt = usvg::Options::default();
    opt.fontdb = Arc::new(fonts().clone());
    opt.font_family = "sans-serif".into();
    let Ok(tree) = usvg::Tree::from_data(data, &opt) else { return 0 };
    let s = tree.size();
    let side = s.width().max(s.height());
    let mut k = 1.0f32;
    if side < min_side as f32 {
        k = min_side as f32 / side;
    }
    if side * k > max_side as f32 {
        k = max_side as f32 / side;
    }
    let (w, h) = (((s.width() * k).round() as u32).max(1), ((s.height() * k).round() as u32).max(1));
    let Some(mut pm) = tiny_skia::Pixmap::new(w, h) else { return 0 };
    resvg::render(&tree, tiny_skia::Transform::from_scale(k, k), &mut pm.as_mut());
    let mut out = Vec::with_capacity(8 + pm.data().len());
    out.extend_from_slice(&w.to_le_bytes());
    out.extend_from_slice(&h.to_le_bytes());
    out.extend_from_slice(pm.data());
    let len = out.len();
    let ptr = out.as_mut_ptr();
    std::mem::forget(out);
    (ptr as u64) << 32 | len as u64
}
