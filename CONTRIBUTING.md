# Contributing to radarpoint

Thanks for your interest! Bug reports, data findings and pull requests are all
welcome.

## Reporting

- **Bugs**: open an issue with the command or request you ran, what you
  expected and what you got.
- **Data findings**: Radar-DPC does not document units, nodata values or
  projections, so what we know comes from inspecting real files. If you notice
  something new (the POH scale, a product we do not handle, a format change),
  open an issue with the product, the time and, if you can, the output of
  `go run ./cmd/tiffdump file.tif`.

## Development setup

You need Go (the version in `go.mod`) and nothing else: no GDAL, no C
compiler.

```
git clone https://github.com/vmaltarello/radarpoint
cd radarpoint
go test ./...
```

Tests never call Radar-DPC: they use local fake servers and the small real
excerpts in `testdata/`. To try your change against live data:

```
go run ./cmd/radarpoint --product SRI --lat 45.5966 --lon 8.915
go run ./cmd/radarpointd            # then open http://localhost:8080/docs
go run ./cmd/nowcastverify          # if you touched internal/nowcast
```

## Pull requests

1. Fork the repository and create a branch from `main`.
2. Keep changes focused: one topic per pull request.
3. Before pushing, run:

   ```
   gofmt -l .        # must print nothing
   go vet ./...
   go test -race ./...
   ```

   The same checks run automatically on every pull request.
4. Add or update tests for what you change. For the nowcast, include the
   output of `go run ./cmd/nowcastverify --cases 20` before and after.
5. Update `README.md` if behaviour, flags or the HTTP API change.

### Commit messages

We follow [Conventional Commits](https://www.conventionalcommits.org/):
a single line, imperative mood, lower case after the colon, no final period.

```
feat(api): add hail probability to /v1/nowcast
fix(raster): handle tiled GeoTIFF files
docs: explain the TEMP nodata mask
test(dpc): cover expired download URLs
```

Common types: `feat`, `fix`, `docs`, `test`, `refactor`, `perf`, `ci`,
`chore`. The scope, in parentheses, is optional and names the area touched
(`api`, `nowcast`, `dpc`, `raster`, …).

### Code style

- Standard Go style, formatted with `gofmt`.
- Prefer the standard library; discuss new dependencies in an issue first.
- Comments explain why, not what; exported identifiers have doc comments.
- Messages, logs and API responses are in English.

## Data and attribution

All radar data comes from Radar-DPC – Dipartimento della Protezione Civile,
under CC-BY-SA 4.0. Any output that shows data must keep the attribution.
New files added to `testdata/` must be excerpts of real Radar-DPC files,
cut with `cmd/tiffcrop`, with their product and time noted in `README.md`.

## License

By contributing you agree that your contributions are released under the
[MIT license](LICENSE) of this project.
