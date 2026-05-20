# Word add-in icons

Drop the production icons here before deploy:

- `icon-16.png`  (ribbon button, normal DPI)
- `icon-32.png`  (ribbon button, high DPI)
- `icon-64.png`  (Office catalog tile, normal DPI)
- `icon-80.png`  (ribbon button, very high DPI / Mac retina)
- `icon-128.png` (Office catalog tile, high DPI)

Match the corresponding paths under `https://addin.vaultdms.example.com/word/assets/`
in `manifest.xml`. PNG with transparent background is the
documented requirement; SVG is not accepted by the Office host.

The dev-loopback (HTTPS :3002) serves whatever's in this folder
verbatim, so for development you can drop placeholder PNGs of any
size and Office will scale them down.
