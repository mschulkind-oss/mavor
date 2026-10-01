# The closure used for the measured Shell/Mutter 50.4 acceptance, not registry
# latest. Test-only: no host session and no jail configuration changes.
let
  source = builtins.fetchTarball {
    url = "https://releases.nixos.org/nixpkgs/nixpkgs-26.11pre1082290.b6c8664de9b6/nixexprs.tar.zst";
    sha256 = "sha256-k8Fu4c9Z+4Nh7mUr0cfw++ITQiyEhlWxoJBOkI3tOcQ=";
  };
  p = import source {};
in p.mkShell {
  packages = [ p.gtk4 p.pkg-config p.gcc p.glib p.xclip p.wl-clipboard p.dbus p.python3 ];
  MAVOR_GNOME_SHELL = "${p.gnome-shell}/bin/gnome-shell";
  GBM_BACKENDS_PATH = "${p.mesa}/lib/gbm";
  LIBGL_DRIVERS_PATH = "${p.mesa}/lib/dri";
  __EGL_VENDOR_LIBRARY_FILENAMES = "${p.mesa}/share/glvnd/egl_vendor.d/50_mesa.json";
  PYTHONDONTWRITEBYTECODE = "1";
}
