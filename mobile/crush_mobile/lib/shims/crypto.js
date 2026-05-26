// No imports here to avoid Babel issues in CJS
module.exports = {
  randomBytes: (size) => {
    const bytes = new Uint8Array(size);
    if (global.crypto && global.crypto.getRandomValues) {
      global.crypto.getRandomValues(bytes);
    } else {
      for (let i = 0; i < size; i++) {
        bytes[i] = Math.floor(Math.random() * 256);
      }
    }
    return bytes;
  },
};
