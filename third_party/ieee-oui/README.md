# IEEE MAC-prefix registries (vendored)

Verbatim-ish captures (CRLF->LF only) of the three IEEE Registration
Authority MAC-prefix registries, fetched by cmd/ouifetch; see
PROVENANCE for source URLs, date, and checksums. cmd/ouigen converts
them to internal/oui/table.txt (the ASCII lookup table keaexporter
embeds).

## License / entitlement

The files are registry FACTS ("prefix X is assigned to organization
Y"). IEEE's own position, carried verbatim in Debian's ieee-data
package, is that "IEEE does not assert any copyright in the OUI Public
Listing or attempt to restrict distribution of the listing in any
way." Debian ships this same data publicly in main (package
ieee-data), whose policy requires freely-redistributable licensing --
a global distributor redistributing this exact dataset on exactly that
basis. Facts are also not copyrightable in the US (Feist v. Rural), and
a comprehensive registry has no creative selection or arrangement to
protect. So vendoring this copy, and redistributing it in this public
repository, is clear.

The data is NOT under this repository's code license (LICENSE): it is
IEEE's public listing, redistributed under IEEE's own no-restriction
statement. PROVENANCE records the source URLs, fetch date, and
checksums. IEEE asks users to obtain the listing from IEEE and refresh
it regularly -- cmd/ouifetch does exactly that. We deliberately vendor
IEEE's raw data rather than Wireshark's nicer "manuf" aggregation to
avoid inheriting Wireshark's GPL-2.0 on the data file.

## Updating

Occasionally (IEEE dislikes bulk-download traffic -- do not cron this):

    go run ./cmd/ouifetch
    go run ./cmd/ouigen

## ASCII exemption

The CSVs contain non-ASCII organization names and are the repo's ONLY
exemption from scripts/check-ascii.sh (the ascii gate is a
habit-enforcement gate for authored text; this is captured data). The
exemption is "validated UTF-8", not "anything goes": cmd/ouifetch
refuses to write a capture that is not strict UTF-8 or that contains
stray control bytes, and cmd/ouigen -verify re-validates the committed
files in every presubmit. This README and PROVENANCE stay inside the
ascii gate -- only *.csv is exempt.
