{ pkgs }:

let
  defaultAlbums = "57783852,796709881,502723,101618,175927162,54628042,11205422,1007321681,1401700,940424";

  run = pkgs.writeShellScriptBin "deeznt-run" ''
    go run . "$@"
  '';

  # Start the daemon with a fixture album list and auto-download enabled.
  # Usage: deeznt-fixture [album_id ...]
  # Then in another terminal: deeznt-sync, deeznt-dl, deeznt-import
  fixture = pkgs.writeShellScriptBin "deeznt-fixture" ''
    if [[ $# -gt 0 ]]; then
      albums=$(IFS=,; echo "$*")
    else
      albums="${defaultAlbums}"
    fi
    DEEZNT_FIXTURE_ALBUMS="$albums" go run . daemon "$@"
  '';

  # Trigger sync in a running daemon.
  sync-cmd = pkgs.writeShellScriptBin "deeznt-sync" ''
    go run . sync start "$@"
  '';

  # Trigger download start in a running daemon.
  dl = pkgs.writeShellScriptBin "deeznt-dl" ''
    go run . download start "$@"
  '';

  # Trigger a full-library beets import in a running daemon.
  import-cmd = pkgs.writeShellScriptBin "deeznt-import" ''
    go run . beets import
  '';

in
pkgs.mkShell {
  buildInputs = with pkgs; [
    go
    gopls
    gotools
    beets
    run
    fixture
    sync-cmd
    dl
    import-cmd
  ];
}
