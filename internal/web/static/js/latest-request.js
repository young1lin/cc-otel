// Monotonic generations for async UI loaders. A loader keeps the returned
// predicate and commits DOM/state only while it is still the newest request for
// that render target. This prevents slower old responses from overwriting fresh UI.
const generations = new Map();
let contextGeneration = 0;

export function invalidateRequestContext() {
    contextGeneration += 1;
}

export function beginLatestRequest(target, { trackContext = true } = {}) {
    const generation = (generations.get(target) || 0) + 1;
    const contextAtStart = contextGeneration;
    generations.set(target, generation);
    return () =>
        generations.get(target) === generation &&
        (!trackContext || contextAtStart === contextGeneration);
}
