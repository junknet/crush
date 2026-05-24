const { getDefaultConfig } = require('expo/metro-config');
const path = require('path');

const config = getDefaultConfig(__dirname);

config.resolver.sourceExts.push('sql');
config.resolver.assetExts.push('gguf', 'raw');

config.resolver.extraNodeModules = {
  ...config.resolver.extraNodeModules,
  crypto: path.resolve(__dirname, 'lib/shims/crypto.js'),
};

module.exports = config;
