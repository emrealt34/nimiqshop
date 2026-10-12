# BIMI sender logo

The sender domain verified in Mailtrap is `shop.nimiqbase.com`. The default
address is `hello@shop.nimiqbase.com`; check the production
`MAILTRAP_FROM_EMAIL` override before configuring a different sender domain.
The display name `nimshop.io` does not change which domain is authenticated.

## Logo

`public/bimi/logo.svg` is a vector trace of the existing
`public/img/brand-icon.png`, rather than an embedded raster image. It preserves
the four overlapping gift cards and uses a solid white square with padding.
It is 160 × 160 pixels, about 15 KB, includes a title and description, and
passes the BIMI Group's official SVG Tiny P/S RNC schema. A schema check is
not certification of the logo or its owner.

The frontend build copies it to `dist/bimi/logo.svg`. Once the updated
frontend is deployed, verify this exact public URL returns the SVG with HTTP
200 and `Content-Type: image/svg+xml`, without a login or HTML challenge:

`https://shop.nimiqbase.com/bimi/logo.svg`

The logo can alternatively be hosted on a public HTTPS asset host. An R2 S3
API endpoint is authenticated storage, not a public URL for email clients.
Do not enable public access to a bucket containing unrelated private files
just to serve this logo.

## DMARC

Mailtrap verifies both `rwmt1._domainkey.shop` and
`rwmt2._domainkey.shop` CNAME records. Its suggested `p=none` DMARC record
verifies a Mailtrap domain but does not satisfy BIMI enforcement.

For this Mailtrap sender subdomain, the BIMI-compatible policy is:

| Cloudflare zone | Type | Name | Content |
| --- | --- | --- | --- |
| `nimiqbase.com` | TXT | `_dmarc.shop` | `v=DMARC1; p=quarantine; rua=mailto:dmarc@smtp-staging.mailtrap.net; ruf=mailto:dmarc@smtp-staging.mailtrap.net; rf=afrf; pct=100` |

Change the existing record; do not add a second DMARC record. This preserves
the existing reporting configuration. Confirm that any additional services
sending as this subdomain pass aligned DKIM or SPF before enforcing DMARC.
The parent domain's separate `_dmarc.nimiqbase.com` policy is not changed.

## Certificate and BIMI record

Gmail requires a VMC or CMC from a recognized certificate authority. Converting
PNG to SVG does not issue this certificate. A VMC generally requires a
registered trademark; CMC eligibility is determined by the issuer. The
certificate issuer must approve the submitted logo and verify domain control.
Do not generate a self-signed certificate or an empty placeholder PEM.

After issuance, publish the **public** entity certificate and the issuer's
required certificate chain as `public/bimi/certificate.pem`. Never publish
a private key, API token or account credentials. The SVG must match the logo
approved and embedded by the issuer. Verify the full PEM is publicly available
over HTTPS before creating this TXT record:

| Cloudflare zone | Type | Name | Content | TTL |
| --- | --- | --- | --- | --- |
| `nimiqbase.com` | TXT | `default._bimi.shop` | `v=BIMI1; l=https://shop.nimiqbase.com/bimi/logo.svg; a=https://shop.nimiqbase.com/bimi/certificate.pem` | 3600 |

The certificate URL above is the intended deployment location, not evidence
that a certificate has been issued. Do not publish this record until both
URLs work and a valid certificate covers the actual sending domain.
A logo-only record can be supported by some other providers, but does not
enable the Gmail logo. Inbox display still depends on the receiving provider;
DNS publication does not guarantee an immediate avatar change.

## Verification

1. Confirm Mailtrap still marks the sender domain and both DKIM records as
   verified. Check an existing delivered message's authentication results for
   aligned DKIM and a DMARC pass; do not send a live email just to preview HTML.
2. Check the effective TXT records for `_dmarc.shop.nimiqbase.com` and
   `default._bimi.shop.nimiqbase.com`; each must have one applicable record.
3. Fetch the SVG and certificate without credentials. Verify the SVG against
   the official Tiny P/S schema and validate the certificate, chain, expiry
   and domain coverage with the issuer's tools.
4. Allow DNS caches and receiving-provider updates to settle. Google states
   that logo appearance can take up to 48 hours after correct publication.

Sources:

- [Google: Set up BIMI](https://knowledge.workspace.google.com/admin/security/set-up-bimi)
- [BIMI Group: SVG schema validation](https://bimigroup.org/using-the-rnc-schema-to-validate-bimi-svg-images/)
- [Mailtrap: Sending domain setup](https://docs.mailtrap.io/email-api-smtp/setup/sending-domain)
- [Cloudflare: R2 token permissions](https://developers.cloudflare.com/r2/api/tokens/)
