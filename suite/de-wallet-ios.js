function headers(r) {
  // The body changes length, so the response goes out chunked.
  delete r.headersOut["Content-Length"];
}

function strip(r, data, flags) {
  // The chunks of one response arrive on the same request object.
  r.dewalletBody = (r.dewalletBody || "") + data;
  if (!flags.last) {
    return;
  }
  var text = r.dewalletBody;
  try {
    var doc = JSON.parse(text);
    delete doc.mtls_endpoint_aliases;
    text = JSON.stringify(doc);
  } catch (e) {
    // Not JSON (an error page): pass it on as it came.
  }
  r.sendBuffer(text, flags);
}

export default { headers, strip };
