// Standalone SVG documents for data: URLs. A document rendered through an
// <img style={{ pointerEvents: 'none' }} draggable={false} src="data:image/svg+xml,…"> must declare the SVG namespace; that
// namespace is the W3C identifier string, a name rather than a network
// location, which is why it is spelled with http — no browser ever requests
// it and the spec does not allow an https spelling. It lives in one exported
// constant so every emitter (this module, <Icon>, the layouts) shares it.
export const SVG_NS = 'http://www.w3.org/2000/svg'; // DevSkim: ignore DS137138 no request is ever made to this URI

export function svgDocument(width: number, height: number, body: string): string {
  return `<svg xmlns="${SVG_NS}" width="${width}" height="${height}">${body}</svg>`;
}

// The same document as an <img>-ready data: URL.
export function svgDataUrl(width: number, height: number, body: string): string {
  return 'data:image/svg+xml;utf8,' + encodeURIComponent(svgDocument(width, height, body));
}
