// Kept as a separate file so the page needs no inline script (CSP script-src 'self').
window.onload = function () {
  window.ui = SwaggerUIBundle({
    url: '/docs/openapi.json',
    dom_id: '#swagger-ui',
    deepLinking: true,
    persistAuthorization: true,
    displayRequestDuration: true,
    docExpansion: 'list',
    defaultModelsExpandDepth: 0,
    presets: [SwaggerUIBundle.presets.apis],
    layout: 'BaseLayout',
  });
};
