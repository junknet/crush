Determines if it is safe to delete the symbol (procedure, template, type, etc.) under the cursor by performing dependency analysis and counting cross-file callers via the custom LSP 'nimlsp/safeToDelete' endpoint.
This checks for all references and cross-file calling graph edges to ensure deleting it has zero blast radius.
