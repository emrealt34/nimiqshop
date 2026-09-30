// Standalone SVG documents for data: URLs. A document rendered through an
// <img src="data:image/svg+xml,…"> must declare the SVG namespace; that
// namespace is the W3C identifier string, a name rather than a network
// location, which is why it is spelled with http.
export function svgDocument(width: number, height: number, body: string): string {
  return `<svg xmlns="http://www.w3.org/2000/svg" width="${width}" height="${height}">${body}</svg>`;
}

// The same document as an <img>-ready data: URL.
export function svgDataUrl(width: number, height: number, body: string): string {
  return 'data:image/svg+xml;utf8,' + encodeURIComponent(svgDocument(width, height, body));
}
