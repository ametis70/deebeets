{ pkgs }:

let
  defaultAlbums = "57783852,796709881,502723,101618,175927162,54628042,11205422,1007321681,1401700,940424";

  run = pkgs.writeShellScriptBin "deebeets-run" ''
    DEEBEETS_SECRET=test go run . daemon "$@"
  '';

  fixture = pkgs.writeShellScriptBin "deebeets-fixture" ''
    if [[ $# -gt 0 ]]; then
      albums=$(IFS=,; echo "$*")
    else
      albums="${defaultAlbums}"
    fi
    DEEBEETS_SECRET=test DEEBEETS_FIXTURE_ALBUMS="$albums" go run . daemon
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
  ];
}
