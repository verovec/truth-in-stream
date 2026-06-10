#!/bin/sh
# Regenerates the committed multistream fixture. A Wikipedia multistream dump
# is a concatenation of independently decompressible bz2 streams: a header
# stream (siteinfo, no pages), N page-cluster streams, the last one carrying
# the closing root tag. The companion index maps "offset:pageid:title" lines
# to the byte offset of the stream containing each page.
#
# Run from this directory: sh gen-fixture.sh
set -eu

cat > s0.xml <<'EOF'
<mediawiki xmlns="http://www.mediawiki.org/xml/export-0.11/" version="0.11" xml:lang="en">
  <siteinfo>
    <sitename>Wikipedia</sitename>
    <dbname>simplewiki</dbname>
  </siteinfo>
EOF

cat > s1.xml <<'EOF'
  <page>
    <title>Paris</title>
    <ns>0</ns>
    <id>1</id>
    <revision>
      <id>100</id>
      <timestamp>2026-01-15T10:30:00Z</timestamp>
      <text bytes="220" xml:space="preserve">'''Paris''' is the capital of [[France]].&lt;ref&gt;Atlas&lt;/ref&gt; It is known for the [[Eiffel Tower]].

The city sits on the [[Seine]] river and has been a major centre of finance, commerce, and culture since the 17th century.

== History ==
Body text that must not reach the stored chunks.</text>
    </revision>
  </page>
  <page>
    <title>The City of Light</title>
    <ns>0</ns>
    <id>2</id>
    <redirect title="Paris" />
    <revision>
      <id>101</id>
      <text bytes="19" xml:space="preserve">#REDIRECT [[Paris]]</text>
    </revision>
  </page>
EOF

cat > s2.xml <<'EOF'
  <page>
    <title>Mercury</title>
    <ns>0</ns>
    <id>3</id>
    <revision>
      <id>102</id>
      <text bytes="64" xml:space="preserve">'''Mercury''' may refer to:
* [[Mercury (planet)]]
* [[Mercury (element)]]
{{disambiguation}}</text>
    </revision>
  </page>
  <page>
    <title>Talk:Paris</title>
    <ns>1</ns>
    <id>4</id>
    <revision>
      <id>103</id>
      <text bytes="11" xml:space="preserve">Discussion.</text>
    </revision>
  </page>
</mediawiki>
EOF

bzip2 -zf s0.xml s1.xml s2.xml

O1=$(wc -c < s0.xml.bz2)
O2=$((O1 + $(wc -c < s1.xml.bz2)))

cat s0.xml.bz2 s1.xml.bz2 s2.xml.bz2 > fixture-multistream.xml.bz2

printf '%s:1:Paris\n%s:2:The City of Light\n%s:3:Mercury\n%s:4:Talk:Paris\n' \
  "$O1" "$O1" "$O2" "$O2" > fixture-multistream-index.txt
bzip2 -zf fixture-multistream-index.txt

rm -f s0.xml.bz2 s1.xml.bz2 s2.xml.bz2

# An empty (but valid) bz2 index: exercises the no-streams guard.
: > fixture-empty-index.txt
bzip2 -zf fixture-empty-index.txt
