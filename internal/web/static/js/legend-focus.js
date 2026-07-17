import { getModelColor } from './theme.js';

// Shared model-isolate legend for the Rate and Intraday charts.
//
// Clicking a legend entry isolates that model; clicking it again — or the
// "All models" button — restores every series. ECharts' own multi-select
// legend cannot express "isolate", so selection is driven from our own state
// and pushed back into the chart.

// legendSelectedMap turns a focus model into ECharts' legend.selected map.
// A null focus selects every model.
export function legendSelectedMap(models, focus) {
    const sel = {};
    for (const m of models) sel[m] = !focus || m === focus;
    return sel;
}

// Past this many models a plain legend wraps and eats the plot area, so it
// switches to a single scrolling row.
const SCROLL_LEGEND_MIN_MODELS = 8;

function useScrollLegend(models) {
    return models.length >= SCROLL_LEGEND_MIN_MODELS;
}

// legendOption builds the legend config shared by both charts.
export function legendOption(models, c, focus) {
    return {
        type: useScrollLegend(models) ? 'scroll' : 'plain',
        top: 4,
        left: 'center',
        width: '92%',
        selectedMode: 'multiple',
        itemGap: 14,
        itemHeight: 10,
        textStyle: { color: c.legendText, fontSize: 11 },
        data: models.map((m) => ({
            name: m,
            icon: 'roundRect',
            itemStyle: { color: getModelColor(m) },
        })),
        selected: legendSelectedMap(models, focus),
    };
}

// legendGridTop reports the vertical room the legend needs. The legend sits
// outside the grid, so the plot must start below it.
export function legendGridTop(models) {
    return useScrollLegend(models) ? 56 : 44;
}

// makeLegendFocus wires isolate behavior to one chart. getFocus/setFocus read
// and write the caller's state key, and getAllBtn resolves the "All models"
// button lazily, which keeps this module free of both state layout and the DOM.
export function makeLegendFocus({ getFocus, setFocus, getAllBtn, onApplied }) {
    function syncAllBtn() {
        const btn = getAllBtn?.();
        if (btn) btn.style.display = getFocus() ? '' : 'none';
    }

    function apply(chart, models) {
        if (getFocus() && !models.includes(getFocus())) setFocus(null);
        // setOption below makes ECharts emit legendselectchanged. Without this
        // guard the handler could not tell our own write from a user click and
        // would toggle the focus straight back off.
        chart.__legendFocusApplying = true;
        chart.setOption({ legend: { selected: legendSelectedMap(models, getFocus()) } });
        chart.__legendFocusApplying = false;
        syncAllBtn();
        onApplied?.(chart);
    }

    function bind(chart, models) {
        chart.off('legendselectchanged');
        chart.on('legendselectchanged', (params) => {
            if (chart.__legendFocusApplying) return;
            setFocus(getFocus() === params.name ? null : params.name);
            apply(chart, models);
        });
    }

    function clear(chart, models) {
        setFocus(null);
        apply(chart, models);
    }

    return { apply, bind, clear, syncAllBtn };
}
