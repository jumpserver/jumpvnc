//! Local synthetic evaluation. Never captures or controls an operating-system desktop.
use rustvncserver::{server::ServerEvent, VncServer};

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let port = std::env::args().nth(1).unwrap_or_else(|| "25904".into()).parse::<u16>()?;
    let (server, mut events) = VncServer::new(640, 360, "svnc rustvncserver 2.2.1 synthetic evaluation".into(), Some("evalonly".into()));
    let fb = server.framebuffer().clone();
    let mut pixels = vec![0u8; 640 * 360 * 4];
    for y in 0..360usize {
        for x in 0..640usize {
            let p = (y * 640 + x) * 4;
            pixels[p] = ((x * 17 + y * 3) % 256) as u8;
            pixels[p + 1] = ((y * 13 + x * 7) % 256) as u8;
            pixels[p + 2] = ((x * y + 97) % 256) as u8;
            pixels[p + 3] = 255;
        }
    }
    fb.update_from_slice(&pixels).await?;
    fb.update_cropped(&vec![0; 96 * 96 * 4], 96, 96, 96, 96).await?;
    tokio::spawn(async move {
        let mut white = false;
        let mut count = 0usize;
        while let Some(event) = events.recv().await {
            match event {
                ServerEvent::KeyPress { down: true, key: 0xffc5, .. }
                | ServerEvent::PointerMove { button_mask: 1, .. } => {
                    white = !white;
                    count += 1;
                    let value = if white { 255 } else { 0 };
                    fb.update_cropped(&vec![value; 96 * 96 * 4], 96, 96, 96, 96).await.expect("valid test rectangle");
                    println!("input_events={count}");
                }
                _ => {}
            }
        }
    });
    println!("SYNTHETIC_ONLY bind=127.0.0.1:{port} turbojpeg=enabled upstream=2.2.1");
    server.listen(port).await?;
    Ok(())
}
