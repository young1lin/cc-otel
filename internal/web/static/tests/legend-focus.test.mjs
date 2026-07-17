import test from 'node:test';
import assert from 'node:assert/strict';
import { legendSelectedMap, legendOption, legendGridTop, makeLegendFocus } from '../js/legend-focus.js';

test('a null focus selects every model', () => {
    assert.deepEqual(legendSelectedMap(['a', 'b'], null), { a: true, b: true });
});

test('a focus selects only that model', () => {
    assert.deepEqual(legendSelectedMap(['a', 'b', 'c'], 'b'), { a: false, b: true, c: false });
});

test('legendOption stays plain up to 7 models and scrolls past that', () => {
    const c = { legendText: '#000' };
    const seven = ['a', 'b', 'c', 'd', 'e', 'f', 'g'];
    assert.equal(legendOption(seven, c, null).type, 'plain');
    assert.equal(legendOption([...seven, 'h'], c, null).type, 'scroll');
});

test('legendOption carries a roundRect swatch per model and honors focus', () => {
    const opt = legendOption(['a', 'b'], { legendText: '#000' }, 'a');
    assert.equal(opt.data.length, 2);
    assert.equal(opt.data[0].name, 'a');
    assert.equal(opt.data[0].icon, 'roundRect');
    assert.ok(opt.data[0].itemStyle.color, 'each entry carries its model color');
    assert.deepEqual(opt.selected, { a: true, b: false });
});

test('legendGridTop reserves more room once the legend scrolls', () => {
    assert.equal(legendGridTop(['a']), 44);
    assert.ok(legendGridTop(['a', 'b', 'c', 'd', 'e', 'f', 'g', 'h']) > legendGridTop(['a']));
});

// Minimal ECharts stand-in: records setOption calls and replays legend clicks.
function fakeChart() {
    const chart = {
        options: [],
        handlers: {},
        setOption(o) { this.options.push(o); },
        off(evt) { delete this.handlers[evt]; },
        on(evt, fn) { this.handlers[evt] = fn; },
        click(name) { this.handlers.legendselectchanged?.({ name }); },
    };
    return chart;
}

function harness(initial) {
    let focus = initial;
    const btn = { style: { display: 'none' } };
    const focusApi = makeLegendFocus({
        getFocus: () => focus,
        setFocus: (v) => { focus = v; },
        getAllBtn: () => btn,
    });
    return { focusApi, getFocus: () => focus, btn };
}

test('clicking a legend entry isolates that model', () => {
    const chart = fakeChart();
    const { focusApi, getFocus } = harness(null);
    focusApi.bind(chart, ['a', 'b']);
    chart.click('a');
    assert.equal(getFocus(), 'a');
    assert.deepEqual(chart.options.at(-1).legend.selected, { a: true, b: false });
});

test('clicking the focused model again restores every model', () => {
    const chart = fakeChart();
    const { focusApi, getFocus } = harness('a');
    focusApi.bind(chart, ['a', 'b']);
    chart.click('a');
    assert.equal(getFocus(), null);
    assert.deepEqual(chart.options.at(-1).legend.selected, { a: true, b: true });
});

test('apply does not re-enter through the event it triggers', () => {
    const chart = fakeChart();
    const { focusApi, getFocus } = harness(null);
    focusApi.bind(chart, ['a', 'b']);
    // Replay ECharts' real behavior: setOption itself emits legendselectchanged.
    chart.setOption = function (o) {
        this.options.push(o);
        this.click('a');
    };
    chart.click('a');
    assert.equal(getFocus(), 'a', 'the echoed event must not toggle focus back off');
});

test('a focus on a model absent from the new data is dropped', () => {
    const chart = fakeChart();
    const { focusApi, getFocus } = harness('gone');
    focusApi.apply(chart, ['a', 'b']);
    assert.equal(getFocus(), null);
    assert.deepEqual(chart.options.at(-1).legend.selected, { a: true, b: true });
});

test('the All models button shows only while a model is isolated', () => {
    const chart = fakeChart();
    const { focusApi, btn } = harness(null);
    focusApi.bind(chart, ['a', 'b']);

    focusApi.apply(chart, ['a', 'b']);
    assert.equal(btn.style.display, 'none', 'hidden when every model is shown');

    chart.click('a');
    assert.equal(btn.style.display, '', 'shown once a model is isolated');
});

test('clear restores every model and hides the button', () => {
    const chart = fakeChart();
    const { focusApi, getFocus, btn } = harness('a');
    focusApi.clear(chart, ['a', 'b']);
    assert.equal(getFocus(), null);
    assert.equal(btn.style.display, 'none');
    assert.deepEqual(chart.options.at(-1).legend.selected, { a: true, b: true });
});
